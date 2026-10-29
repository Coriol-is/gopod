package ipc

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/store"
)

// artifactReconcileStore is the store surface of Reconcile.
type artifactReconcileStore interface {
	ExpiredPreparations(ctx context.Context, now time.Time) ([]string, error)
	ListDeletingArtifacts(ctx context.Context, afterID string, limit int) ([]store.DeletingArtifact, error)
	CompleteArtifactDeletion(ctx context.Context, id string) error
	MarkArtifactsForGC(ctx context.Context, now time.Time, retention time.Duration, limit int) (int, error)
	ReadyArtifacts(ctx context.Context, afterID string, limit int) ([]store.Artifact, error)
	QuarantineArtifact(ctx context.Context, id, reason string) (int, error)
	QuarantinedArtifacts(ctx context.Context, afterID string, limit int) ([]store.Artifact, error)
	RestoreArtifact(ctx context.Context, id string) error
	KnownPreparation(ctx context.Context, prep string) (bool, error)
}

// ReconcileReport counts what one Reconcile pass did.
type ReconcileReport struct {
	ExpiredPreparations int
	DeletedBlobs        int
	GCMarked            int
	Quarantined         int
	Restored            int
	OrphanDirs          int
}

// reconcileBatch is a variable only so tests can shrink the page size.
var reconcileBatch = 100

const (
	// orphanGrace keeps a directory the inventory does not know (for example
	// after a database restore) long enough that an in-flight preparation, whose
	// reservation is committed first, can never be mistaken for one.
	orphanGrace = 2 * time.Hour
)

// Reconcile is the startup and periodic crash reconciliation. It never follows
// links, only touches host-owned UUID preparation directories, and never deletes
// a blob whose artifact is not marked deleting by the store (which refuses to
// do so for a pending, sending or uncertain reference):
//
//  1. expire preparations whose reservation deadline passed;
//  2. finish every deleting artifact (unlink the blob, then drop the row);
//  3. mark terminal artifacts past retention for deletion, then finish them;
//  4. quarantine ready artifacts whose blob is missing or the wrong size, which
//     also fails their still-pending parts visibly;
//  5. remove unknown preparation directories older than a grace period.
func (a ArtifactConfig) Reconcile(ctx context.Context, now time.Time, log *slog.Logger) (ReconcileReport, error) {
	var rep ReconcileReport
	if !a.enabled() {
		return rep, nil
	}
	var errs []error
	ids, err := a.Store.ExpiredPreparations(ctx, now)
	if err != nil {
		return rep, fmt.Errorf("expire preparations: %w", err)
	}
	rep.ExpiredPreparations = len(ids)

	n, err := a.finishDeleting(ctx, log)
	rep.DeletedBlobs += n
	errs = append(errs, err)

	marked, err := a.Store.MarkArtifactsForGC(ctx, now, a.Retention, reconcileBatch)
	rep.GCMarked = marked
	errs = append(errs, err)
	if marked > 0 {
		n, err = a.finishDeleting(ctx, log)
		rep.DeletedBlobs += n
		errs = append(errs, err)
	}

	q, err := a.verifyReady(ctx, log)
	rep.Quarantined = q
	errs = append(errs, err)

	r, err := a.restoreQuarantined(ctx, log)
	rep.Restored = r
	errs = append(errs, err)

	o, err := a.removeOrphans(ctx, now, log)
	rep.OrphanDirs = o
	errs = append(errs, err)
	return rep, errors.Join(errs...)
}

// finishDeleting unlinks the blob of every deleting artifact and then removes
// its row. A crash between the two steps is finished by the next call.
func (a ArtifactConfig) finishDeleting(ctx context.Context, log *slog.Logger) (int, error) {
	done, after := 0, ""
	for {
		batch, err := a.Store.ListDeletingArtifacts(ctx, after, reconcileBatch)
		if err != nil {
			return done, err
		}
		for _, d := range batch {
			after = d.ID // page past a failing row instead of retrying it forever
			if err = a.Blobs.RemoveBlob(d.BlobKey); err != nil {
				log.Warn("ipc: remove artifact blob", "artifact", d.ID, "err", err)
				continue
			}
			if err = a.Store.CompleteArtifactDeletion(ctx, d.ID); err != nil {
				log.Warn("ipc: complete artifact deletion", "artifact", d.ID, "err", err)
				continue
			}
			done++
		}
		if len(batch) < reconcileBatch {
			return done, nil
		}
	}
}

// verifyReady checks, one batch at a time, that every ready artifact still has
// a single-link blob of the inventoried size. Hash verification happens when the
// bytes are opened for upload, so a silent corruption is quarantined there
// without any upload.
func (a ArtifactConfig) verifyReady(ctx context.Context, log *slog.Logger) (int, error) {
	// A blob root with no preparation at all while ready rows exist is a root
	// that was recreated, restored empty or remounted wrongly. Quarantining
	// every artifact on that evidence would turn one operator mistake into a
	// permanent loss of pending deliveries, so hold the sweep and say so.
	if first, err := a.Store.ReadyArtifacts(ctx, "", 1); err != nil {
		return 0, err
	} else if len(first) > 0 {
		dirs, derr := a.Blobs.ListPreparations()
		if derr != nil {
			return 0, derr
		}
		if len(dirs) == 0 {
			log.Error("ipc: artifact blob root is empty but the inventory has ready artifacts; verification sweep held (restore the blob root, or the artifacts quarantine one by one at upload)")
			return 0, nil
		}
	}
	quarantined, after := 0, ""
	for {
		batch, err := a.Store.ReadyArtifacts(ctx, after, reconcileBatch)
		if err != nil {
			return quarantined, err
		}
		for _, art := range batch {
			after = art.ID
			size, serr := a.Blobs.StatBlob(art.BlobKey)
			reason := ""
			switch {
			case errors.Is(serr, artifactfs.ErrNotFound), errors.Is(serr, artifactfs.ErrPreparationMissing):
				reason = "blob_missing"
			case errors.Is(serr, artifactfs.ErrCorrupt), errors.Is(serr, artifactfs.ErrUnsafePath):
				reason = "blob_corrupt"
			case serr != nil:
				log.Warn("ipc: stat artifact blob", "artifact", art.ID, "err", serr)
				continue
			case size != art.Size:
				reason = "blob_size_mismatch"
			}
			if reason == "" {
				continue
			}
			failed, qerr := a.Store.QuarantineArtifact(ctx, art.ID, reason)
			if qerr != nil {
				return quarantined, qerr
			}
			log.Warn("ipc: artifact quarantined", "artifact", art.ID, "reason", reason, "failed_parts", failed)
			quarantined++
		}
		if len(batch) < reconcileBatch {
			return quarantined, nil
		}
	}
}

// removeOrphans removes preparation directories that no reservation or artifact
// row names, once they are older than the grace period.
func (a ArtifactConfig) removeOrphans(ctx context.Context, now time.Time, log *slog.Logger) (int, error) {
	dirs, err := a.Blobs.ListPreparations()
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, d := range dirs {
		if now.Sub(d.ModTime) < orphanGrace {
			continue
		}
		known, kerr := a.Store.KnownPreparation(ctx, d.Name)
		if kerr != nil {
			return removed, kerr
		}
		if known {
			continue
		}
		if rerr := a.Blobs.RemovePreparation(d.Name); rerr != nil {
			log.Warn("ipc: remove orphan preparation", "preparation", d.Name, "err", rerr)
			continue
		}
		removed++
	}
	return removed, nil
}

// restoreQuarantined returns quarantined artifacts to ready when their blob
// verifies again (size and SHA-256), for example after the blob root was
// restored. Parts already failed because of the quarantine stay failed.
func (a ArtifactConfig) restoreQuarantined(ctx context.Context, log *slog.Logger) (int, error) {
	restored, after := 0, ""
	for {
		batch, err := a.Store.QuarantinedArtifacts(ctx, after, reconcileBatch)
		if err != nil {
			return restored, err
		}
		for _, art := range batch {
			after = art.ID
			if size, serr := a.Blobs.StatBlob(art.BlobKey); serr != nil || size != art.Size {
				continue // cheap check first: only hash blobs that could match
			}
			if _, rerr := a.Blobs.ReadVerified(art.BlobKey, art.Size, art.SHA256, art.Size); rerr != nil {
				continue
			}
			if rerr := a.Store.RestoreArtifact(ctx, art.ID); rerr != nil {
				return restored, rerr
			}
			log.Warn("ipc: artifact blob verifies again; restored", "artifact", art.ID)
			restored++
		}
		if len(batch) < reconcileBatch {
			return restored, nil
		}
	}
}
