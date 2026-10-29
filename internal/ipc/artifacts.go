package ipc

// Durable V1 admission with immutable artifacts (CP-003 PR 4).
//
// Order of effects, none of which spans a SQL transaction and file I/O:
//
//  1. converge on an already accepted request (before anything else, so an
//     accepted request survives the end of its run);
//  2. strict parse, capability, actor/destination/scope authorization;
//  3. reserve the full upper bound of bytes in the store;
//  4. outside SQL: walk each source without following links, copy it into a
//     host-private snapshot while hashing, fsync, re-read and compare;
//  5. revalidate capability and scope, mark the inventory ready;
//  6. one store transaction publishes the plan and attaches the references;
//  7. receipt, then archive, exactly as the text route.
//
// The filesystem and SQLite are not one atomic resource. A crash anywhere
// leaves either an expired reservation with a private directory (reconciled by
// ArtifactConfig.Reconcile) or a committed delivery whose receipt is recreated
// from durable state. Nothing here re-reads a source after publication.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

// ArtifactStore is the part of the store the artifact route needs.
type ArtifactStore interface {
	DurableStore
	ValidateConversation(ctx context.Context, expected store.ConversationScope) error
	FindAcceptedRequest(ctx context.Context, sc store.ScopeRef, id store.RequestIdentity) (store.Delivery, bool, error)
	ReserveArtifacts(ctx context.Context, req store.ArtifactReservation) (store.Preparation, error)
	MarkArtifactsReady(ctx context.Context, sc store.ScopeRef, prep string, facts []store.ArtifactFacts) error
	PublishDeliveryWithArtifacts(ctx context.Context, req store.PublishRequest, prep string) (store.Delivery, error)
	AbandonPreparation(ctx context.Context, prep, reason string) error
	ArtifactLimits() store.ArtifactLimits
	artifactReconcileStore
}

// ArtifactConfig enables V1 requests. All of Store, Blobs and Caps must be set
// and Config.Durable must be enabled; otherwise V1 requests keep the legacy path.
type ArtifactConfig struct {
	Store ArtifactStore
	Blobs *artifactfs.Blobs
	Caps  *Capabilities
	// MinFreeBytes is the free-space floor on the blob volume (default 256 MiB).
	MinFreeBytes uint64
	// ReconcileEvery is the reconcile cadence (default 5 minutes).
	ReconcileEvery time.Duration
	// Retention is how long terminal artifacts are kept (default 30 days).
	Retention time.Duration
	// MaxPrepsPerTick bounds file-bearing requests prepared per folder per
	// watcher tick (default 2).
	MaxPrepsPerTick int
	// Fault is a test-only fault-injection hook; ErrSimulatedCrash aborts
	// without cleanup, any other error is an ordinary failure.
	Fault artifactfs.FaultFunc
}

const defaultMinFreeBytes = 256 << 20

// prepBudget is the number of file-bearing requests prepared per folder per tick.
func (a ArtifactConfig) prepBudget() int {
	if a.MaxPrepsPerTick <= 0 {
		return 2
	}
	return a.MaxPrepsPerTick
}

func (a ArtifactConfig) enabled() bool { return a.Store != nil && a.Blobs != nil && a.Caps != nil }

func (a ArtifactConfig) minFree() uint64 {
	if a.MinFreeBytes == 0 {
		return defaultMinFreeBytes
	}
	return a.MinFreeBytes
}

func (a ArtifactConfig) fault(point string) error {
	if a.Fault == nil {
		return nil
	}
	return a.Fault(point)
}

func (w *Watcher) artifactsEnabled() bool {
	return w.cfg.Artifacts.enabled() && w.cfg.Durable.Store != nil && w.cfg.Durable.PersonalJID != ""
}

func sameChat(a, b string) bool {
	return a != "" && strings.TrimPrefix(a, "tg:") == strings.TrimPrefix(b, "tg:")
}

// processArtifactRequest admits one V1 request. It returns true when the file
// reached a terminal local state, like processDurable.
func (w *Watcher) processArtifactRequest(ctx context.Context, folder, filePath string, raw []byte) bool {
	a := w.cfg.Artifacts
	id := requestIdentity(filePath, raw)

	src, err := a.Store.SnapshotScope(ctx, folder)
	if errors.Is(err, store.ErrStaleScope) {
		return w.finishRejected(folder, filePath, codeRecipientUnavailable)
	}
	if err != nil {
		return w.durableTransient(folder, filePath, codeUnavailable, err)
	}
	// Converge on an accepted request first: its capability may be long gone.
	switch d, ok, ferr := a.Store.FindAcceptedRequest(ctx, src, id); {
	case errors.Is(ferr, store.ErrRequestConflict):
		return w.finishConflict(folder, filePath)
	case errors.Is(ferr, store.ErrRequestRejected):
		return w.finishRejected(folder, filePath, rejectionCode(ferr))
	case ferr != nil:
		return w.durableTransient(folder, filePath, codeUnavailable, ferr)
	case ok:
		d.Created = false
		return w.finishQueued(ctx, folder, filePath, d)
	}

	req, err := parseArtifactRequest(raw)
	if err != nil {
		code, _ := errCode(err)
		return w.rejectDurable(ctx, src, folder, filePath, id, code)
	}
	written, _ := w.requestModTime(folder, filePath)
	grant, err := a.Caps.ResolveAt(req.Capability, folder, written)
	if err != nil {
		return w.rejectDurable(ctx, src, folder, filePath, id, codeStaleCapability)
	}
	if code := w.authorizeArtifactGrant(ctx, a, grant, req, folder, src); code != "" {
		if code == codeUnavailable {
			return w.durableTransient(folder, filePath, code, errors.New("scope validation unavailable"))
		}
		return w.rejectDurable(ctx, src, folder, filePath, id, code)
	}
	if w.requestExpired(folder, filePath) {
		return w.rejectDurable(ctx, src, folder, filePath, id, codeRequestExpired)
	}
	if n := len(splitTextIf(req.Text)) + len(req.Files); n > store.MaxDeliveryParts {
		return w.rejectDurable(ctx, src, folder, filePath, id, codeTooManyParts)
	}

	var prep store.Preparation
	if len(req.Files) > 0 {
		// Bounded I/O per folder per tick: a flood of file requests waits for
		// the next tick instead of starving the rest of the watcher.
		if w.artifactPreps >= a.prepBudget() {
			return false
		}
		w.artifactPreps++
		prep, err = w.prepareArtifacts(ctx, a, grant, req, src, written)
		if errors.Is(err, artifactfs.ErrSimulatedCrash) {
			return false
		}
		if err != nil {
			return w.artifactFailure(ctx, a, folder, filePath, src, id, prep, err)
		}
	}
	parts, err := artifactParts(req, prep.ArtifactIDs)
	if err != nil {
		return w.artifactFailure(ctx, a, folder, filePath, src, id, prep, err)
	}
	pr := store.PublishRequest{
		Conversation: grant.Destination,
		Owner:        store.OwnerLegacyAgentIPC,
		ActorRef:     "ipc:" + src.NamespaceID,
		RunRef:       grant.RunRef,
		Parts:        parts,
		Request:      &id,
	}
	if ferr := a.fault("before_publish"); ferr != nil {
		if errors.Is(ferr, artifactfs.ErrSimulatedCrash) {
			return false
		}
		return w.artifactFailure(ctx, a, folder, filePath, src, id, prep, ferr)
	}
	var delivery store.Delivery
	if len(req.Files) == 0 {
		delivery, err = a.Store.PublishDelivery(ctx, pr)
	} else {
		delivery, err = a.Store.PublishDeliveryWithArtifacts(ctx, pr, prep.ID)
	}
	if err != nil {
		return w.artifactFailure(ctx, a, folder, filePath, src, id, prep, err)
	}
	if !delivery.Created && len(req.Files) > 0 {
		// Duplicate: the store already abandoned this preparation; remove its blobs.
		w.cleanupPreparation(ctx, a)
	}
	if ferr := a.fault("after_publish"); errors.Is(ferr, artifactfs.ErrSimulatedCrash) {
		return false
	}
	return w.finishQueued(ctx, folder, filePath, delivery)
}

func splitTextIf(text string) []store.DeliveryPart {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	return splitText(text)
}

// authorizeArtifactGrant is the actor/destination/scope gate for a capability.
// It returns "" when allowed, codeUnavailable for a transient store failure and
// a sanitized rejection code otherwise.
func (w *Watcher) authorizeArtifactGrant(ctx context.Context, a ArtifactConfig, g Grant, req artifactRequest, folder string, src store.ScopeRef) string {
	dest := g.Destination
	switch {
	case g.Owner != store.OwnerLegacyAgentIPC, // controller-owned runs cannot take a second publication
		!w.durableActor(folder), !w.durableDestination(dest.Scope.JID),
		req.ChatJID != "" && !sameChat(req.ChatJID, dest.Scope.JID),
		dest.Scope.NamespaceID != src.NamespaceID, dest.Scope.NamespaceEpoch != src.NamespaceEpoch:
		return codeRecipientUnavailable
	}
	err := a.Store.ValidateConversation(ctx, dest)
	switch {
	case err == nil:
		return ""
	case errors.Is(err, store.ErrStaleScope):
		return codeRecipientUnavailable
	}
	return codeUnavailable
}

// prepareArtifacts reserves bytes, snapshots every file and marks the inventory
// ready. On error the returned Preparation (possibly zero) must be cleaned up by
// the caller through artifactFailure.
func (w *Watcher) prepareArtifacts(ctx context.Context, a ArtifactConfig, g Grant, req artifactRequest, src store.ScopeRef, written time.Time) (store.Preparation, error) {
	lim := a.Store.ArtifactLimits()
	rels := make([]string, len(req.Files))
	for i, f := range req.Files {
		rel, err := g.MapPath(f.Path)
		if err != nil {
			return store.Preparation{}, reject(codePathDenied)
		}
		rels[i] = rel
	}
	reserve := int64(len(req.Files)) * lim.MaxFileBytes
	if reserve > lim.MaxRequestBytes {
		reserve = lim.MaxRequestBytes
	}
	if free, err := a.Blobs.FreeBytes(); err != nil {
		return store.Preparation{}, err
	} else if free < a.minFree()+uint64(reserve) {
		return store.Preparation{}, reject(codeStorageFull)
	}
	prep, err := a.Store.ReserveArtifacts(ctx, store.ArtifactReservation{Scope: src, OwnerRef: g.RunRef, Files: len(req.Files), Bytes: reserve})
	if errors.Is(err, store.ErrArtifactQuotaExceeded) {
		return store.Preparation{}, reject(codeQuotaExceeded)
	}
	if errors.Is(err, store.ErrArtifactRateLimited) {
		return store.Preparation{}, reject(codeRateLimited)
	}
	if err != nil {
		return store.Preparation{}, err
	}
	if err = a.fault("after_reserve"); err != nil {
		return prep, err
	}
	if err = a.Blobs.NewPreparation(prep.ID); err != nil {
		return prep, hostFault(err)
	}
	root, err := artifactfs.OpenSourceRoot(g.OutputRoot)
	if err != nil {
		return prep, hostFault(err)
	}
	defer root.Close()

	facts := make([]store.ArtifactFacts, len(req.Files))
	var total int64
	for i, f := range req.Files {
		// Never write past the reservation: the bound is what is left of it.
		limit := min(lim.MaxFileBytes, reserve-total)
		if limit <= 0 {
			return prep, reject(codeFileTooLarge)
		}
		file, oerr := root.Open(rels[i], limit)
		if oerr != nil {
			return prep, mapPrepareError(oerr)
		}
		if file.Size() == 0 {
			file.Close()
			return prep, reject(codeEmptyFile)
		}
		_, name, _ := strings.Cut(prep.BlobKeys[i], "/")
		snap, serr := a.Blobs.Snapshot(prep.ID, name, file, limit)
		file.Close()
		if serr != nil {
			return prep, hostFault(serr)
		}
		total += snap.Size
		if total > reserve {
			return prep, reject(codeFileTooLarge)
		}
		mime := f.Mime
		if mime == "" {
			mime = "application/octet-stream"
		}
		facts[i] = store.ArtifactFacts{ArtifactID: prep.ArtifactIDs[i], SHA256: snap.SHA256, Size: snap.Size, MediaType: mime,
			Filename: sanitizeFilename(f.Filename, path.Base(f.Path))}
	}
	if err = a.fault("before_mark_ready"); err != nil {
		return prep, err
	}
	// Revalidate capability and scope after the (possibly slow) copy.
	if _, rerr := a.Caps.ResolveAt(req.Capability, g.Folder, written); rerr != nil {
		return prep, reject(codeStaleCapability)
	}
	if code := w.authorizeArtifactGrant(ctx, a, g, req, g.Folder, src); code != "" {
		if code == codeUnavailable {
			return prep, errors.New("scope validation unavailable")
		}
		return prep, reject(code)
	}
	if err = a.Store.MarkArtifactsReady(ctx, src, prep.ID, facts); err != nil {
		if errors.Is(err, store.ErrStaleScope) {
			return prep, reject(codeRecipientUnavailable)
		}
		return prep, err
	}
	return prep, a.fault("after_mark_ready")
}

// hostFault classifies a failure of a host-side step (the preparation directory,
// the output root, the snapshot write). A missing path there is the host's
// problem and is retried; it is never reported as a missing agent file. Link,
// special-file, size and change errors keep their sanitized codes.
func hostFault(err error) error {
	if errors.Is(err, artifactfs.ErrNotFound) {
		return fmt.Errorf("host-side path missing: %w", artifactfs.ErrPreparationMissing)
	}
	return mapPrepareError(err)
}

// mapPrepareError turns filesystem errors into sanitized rejection codes. A
// simulated crash and unknown errors pass through unchanged.
func mapPrepareError(err error) error {
	switch {
	case errors.Is(err, artifactfs.ErrSimulatedCrash), errors.Is(err, artifactfs.ErrPreparationMissing):
		return err
	case errors.Is(err, artifactfs.ErrUnsafePath):
		return reject(codePathDenied)
	case errors.Is(err, artifactfs.ErrNotRegular):
		return reject(codeNotRegularFile)
	case errors.Is(err, artifactfs.ErrHardlinked):
		return reject(codeHardlinkDenied)
	case errors.Is(err, artifactfs.ErrTooLarge):
		return reject(codeFileTooLarge)
	case errors.Is(err, artifactfs.ErrNotFound):
		return reject(codeSourceMissing)
	case errors.Is(err, artifactfs.ErrSourceChanged):
		return reject(codeSourceChanged)
	case errors.Is(err, artifactfs.ErrStorageFull):
		return reject(codeStorageFull)
	}
	return err
}

// artifactFailure cleans up an aborted preparation and chooses between a
// durable rejection, a bounded retry and a transient retry. A simulated crash
// leaves everything as it is.
func (w *Watcher) artifactFailure(ctx context.Context, a ArtifactConfig, folder, filePath string, src store.ScopeRef, id store.RequestIdentity, prep store.Preparation, err error) bool {
	if errors.Is(err, artifactfs.ErrSimulatedCrash) {
		return false
	}
	if prep.ID != "" {
		if aerr := a.Store.AbandonPreparation(ctx, prep.ID, "admission_failed"); aerr != nil {
			w.log.Warn("ipc: abandon preparation; reconcile will expire it", "folder", folder, "err", aerr)
		}
		w.cleanupPreparation(ctx, a)
	}
	switch {
	case errors.Is(err, store.ErrRequestConflict):
		return w.finishConflict(folder, filePath)
	case errors.Is(err, store.ErrRequestRejected):
		return w.finishRejected(folder, filePath, rejectionCode(err))
	case errors.Is(err, store.ErrOutboxQuotaExceeded):
		return w.retryOrReject(ctx, src, folder, filePath, id, codeQuotaExceeded)
	case errors.Is(err, store.ErrInvalidDelivery), errors.Is(err, store.ErrInvalidArtifact):
		return w.rejectDurable(ctx, src, folder, filePath, id, codeInvalidRequest)
	}
	if code, ok := errCode(err); ok {
		if code == codeQuotaExceeded || code == codeStorageFull {
			return w.retryOrReject(ctx, src, folder, filePath, id, code)
		}
		return w.rejectDurable(ctx, src, folder, filePath, id, code)
	}
	// Includes a stale scope or capability race, an expired preparation and
	// store errors: the next pass takes a fresh look instead of failing.
	return w.durableTransient(folder, filePath, codeUnavailable, err)
}

// retryOrReject keeps the request for a bounded number of passes, then rejects.
func (w *Watcher) retryOrReject(ctx context.Context, src store.ScopeRef, folder, filePath string, id store.RequestIdentity, code string) bool {
	if w.bumpRetry(filePath) {
		return w.rejectDurable(ctx, src, folder, filePath, id, code)
	}
	w.writeDurableReceipt(folder, filePath, durableReceipt{Status: "retrying", Error: code})
	return false
}

// cleanupPreparation removes the blobs of every artifact marked deleting. It
// logs and continues; whatever it misses is finished by Reconcile.
func (w *Watcher) cleanupPreparation(ctx context.Context, a ArtifactConfig) {
	if _, err := a.finishDeleting(ctx, w.log); err != nil {
		w.log.Warn("ipc: remove abandoned artifact blobs; reconcile will retry", "err", err)
	}
}

// maybeReconcileArtifacts runs Reconcile on its cadence.
func (w *Watcher) maybeReconcileArtifacts(ctx context.Context) {
	if !w.artifactsEnabled() {
		return
	}
	every := w.cfg.Artifacts.ReconcileEvery
	if every <= 0 {
		every = 5 * time.Minute
	}
	now := w.cfg.NowFn()
	if !w.lastArtifactReconcile.IsZero() && now.Sub(w.lastArtifactReconcile) < every {
		return
	}
	w.lastArtifactReconcile = now
	rep, err := w.cfg.Artifacts.Reconcile(ctx, now, w.log)
	if err != nil {
		w.log.Warn("ipc: artifact reconcile", "err", err)
		return
	}
	if rep != (ReconcileReport{}) {
		w.log.Info("ipc: artifact reconcile", slog.Any("report", rep))
	}
}
