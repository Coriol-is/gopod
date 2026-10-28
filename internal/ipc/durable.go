package ipc

// Durable text IPC admission (CP-002 PR 3).
//
// A compatible V0 text request from the owner's personal chat is frozen into a
// delivery plan and committed to the outbox instead of being sent directly. The
// store keys the request by namespace + epoch + filename and the SHA-256 of the
// exact bytes, so a restart between commit, receipt and archive converges on the
// same delivery and never publishes twice. "Accepted" means queued, not sent.
//
// The destination scope and the actor grant are separate questions:
//   - actor grant: only the host-selected owner folder may use the route;
//   - destination scope: only the owner's personal chat, thread 0, snapshotted
//     from the live registration (a folder name alone proves nothing).
//
// Every other request keeps the legacy direct-send path unchanged, including
// group and shared chats (CP-020 isolation is not relabelled). The route is
// off unless the host sets Config.Durable.Store, which it does only when the
// dispatcher is running.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
)

// DurableStore is the part of the store the durable route needs.
type DurableStore interface {
	SnapshotScope(ctx context.Context, folder string) (store.ScopeRef, error)
	SnapshotConversation(ctx context.Context, jid string, thread int, visibility store.Visibility) (store.ConversationScope, error)
	PublishDelivery(ctx context.Context, req store.PublishRequest) (store.Delivery, error)
	RecordRequestRejection(ctx context.Context, sc store.ScopeRef, req store.RequestIdentity, reason string) (bool, error)
	RecentIPCRequests(ctx context.Context, sc store.ScopeRef, limit int) ([]store.IPCRequestStatus, error)
	LookupIPCRequest(ctx context.Context, sc store.ScopeRef, key string) (store.IPCRequestStatus, bool, error)
	DeliveryDiagnostic(ctx context.Context, id string) (store.DeliveryDiagnostic, error)
}

// DurableConfig enables the durable route. A nil Store disables it.
type DurableConfig struct {
	Store DurableStore
	// PersonalJID is the only eligible destination: the owner's personal chat
	// in canonical tg:<id> form.
	PersonalJID string
}

// textPartBytes mirrors the legacy sink's UTF-8-safe split, well below
// Telegram's character limit.
const textPartBytes = 3500

// Sanitized rejection codes. They are persisted and shown in receipts, so they
// never carry paths, raw errors or message text.
const (
	codeRecipientUnavailable = "recipient_unavailable"
	codeEmptyText            = "empty_text"
	codeRequestExpired       = "request_expired"
	codeRequestConflict      = "request_conflict"
	codeQuotaExceeded        = "quota_exceeded"
	codeInvalidRequest       = "invalid_request"
	codeUnavailable          = "durable_unavailable"
)

// durableEligible reports whether this request takes the durable route. The
// actor grant and the destination scope are checked separately.
func (w *Watcher) durableEligible(folder, chatJID string) bool {
	d := w.cfg.Durable
	if d.Store == nil || d.PersonalJID == "" {
		return false
	}
	return w.durableActor(folder) && w.durableDestination(chatJID)
}

// durableActor is the actor grant: the host-selected owner folder, still bound
// to the personal chat. It says nothing about which chat may be addressed.
func (w *Watcher) durableActor(folder string) bool {
	if folder == "" || folder != w.cfg.OwnerFolder || w.resolve == nil {
		return false
	}
	jid, ok := w.resolve(folder)
	return ok && w.durableDestination(jid)
}

// durableDestination is the personal-only gate. Legacy bare numeric ids stay
// accepted for the same chat.
func (w *Watcher) durableDestination(chatJID string) bool {
	p := w.cfg.Durable.PersonalJID
	return chatJID != "" && (chatJID == p || chatJID == strings.TrimPrefix(p, "tg:"))
}

func requestIdentity(path string, raw []byte) store.RequestIdentity {
	sum := sha256.Sum256(raw)
	return store.RequestIdentity{Key: filepath.Base(path), PayloadHash: hex.EncodeToString(sum[:])}
}

// splitText cuts text into UTF-8-safe parts of at most textPartBytes.
func splitText(text string) []store.DeliveryPart {
	var parts []store.DeliveryPart
	for len(text) > textPartBytes {
		cut := textPartBytes
		for !utf8.RuneStart(text[cut]) {
			cut--
		}
		parts = append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(text[:cut])})
		text = text[cut:]
	}
	return append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(text)})
}

// processDurable admits one request. It returns true when the file reached a
// terminal local state (receipt written and file archived). Any persistence
// failure leaves the file in place: the next pass converges on the same
// durable record.
func (w *Watcher) processDurable(ctx context.Context, folder, path string, raw []byte, p messagePayload) bool {
	d := w.cfg.Durable.Store
	id := requestIdentity(path, raw)

	src, err := d.SnapshotScope(ctx, folder)
	if errors.Is(err, store.ErrStaleScope) {
		return w.finishRejected(folder, path, codeRecipientUnavailable)
	}
	if err != nil {
		return w.durableTransient(folder, path, codeUnavailable, err)
	}
	conv, err := d.SnapshotConversation(ctx, w.cfg.Durable.PersonalJID, 0, store.VisibilityConversation)
	if errors.Is(err, store.ErrStaleScope) {
		return w.rejectDurable(ctx, src, folder, path, id, codeRecipientUnavailable)
	}
	if err != nil {
		return w.durableTransient(folder, path, codeUnavailable, err)
	}
	if conv.Scope.NamespaceID != src.NamespaceID || conv.Scope.NamespaceEpoch != src.NamespaceEpoch {
		return w.rejectDurable(ctx, src, folder, path, id, codeRecipientUnavailable)
	}
	if strings.TrimSpace(p.Text) == "" {
		return w.rejectDurable(ctx, src, folder, path, id, codeEmptyText)
	}
	if w.requestExpired(folder, path) && !w.alreadyAccepted(ctx, src, id.Key) {
		return w.rejectDurable(ctx, src, folder, path, id, codeRequestExpired)
	}

	delivery, err := d.PublishDelivery(ctx, store.PublishRequest{
		Conversation: conv,
		Owner:        store.OwnerLegacyAgentIPC,
		ActorRef:     "ipc:" + src.NamespaceID,
		Parts:        splitText(p.Text),
		Request:      &id,
	})
	switch {
	case err == nil:
		return w.finishQueued(ctx, folder, path, delivery)
	case errors.Is(err, store.ErrRequestConflict):
		return w.finishConflict(folder, path)
	case errors.Is(err, store.ErrRequestRejected):
		return w.finishRejected(folder, path, rejectionCode(err))
	case errors.Is(err, store.ErrInvalidDelivery):
		return w.rejectDurable(ctx, src, folder, path, id, codeInvalidRequest)
	case errors.Is(err, store.ErrOutboxQuotaExceeded):
		if w.bumpRetry(path) {
			return w.rejectDurable(ctx, src, folder, path, id, codeQuotaExceeded)
		}
		w.writeDurableReceipt(folder, path, durableReceipt{Status: "retrying", Error: codeQuotaExceeded})
		return false
	default:
		// Includes a scope that went stale between snapshot and publish: the
		// next pass takes a fresh snapshot instead of failing the request.
		return w.durableTransient(folder, path, codeUnavailable, err)
	}
}

// durableTransient keeps the file for a later pass. After the retry budget the
// request is failed without a tombstone: nothing was committed, so a resubmit
// is safe.
func (w *Watcher) durableTransient(folder, path, code string, cause error) bool {
	if w.bumpRetry(path) {
		w.log.Error("ipc: durable admission retries exhausted", "folder", folder, "file", filepath.Base(path), "err", cause)
		return w.finishRejected(folder, path, code)
	}
	w.log.Warn("ipc: durable admission transient error", "folder", folder, "file", filepath.Base(path), "attempts", w.retries[path].attempts, "err", cause)
	w.writeDurableReceipt(folder, path, durableReceipt{Status: "retrying", Error: code})
	return false
}

// rejectDurable persists the rejection tombstone before the receipt and the
// archive, so a retry of the same bytes stays rejected after any crash.
func (w *Watcher) rejectDurable(ctx context.Context, src store.ScopeRef, folder, path string, id store.RequestIdentity, code string) bool {
	_, err := w.cfg.Durable.Store.RecordRequestRejection(ctx, src, id, code)
	switch {
	case err == nil:
	case errors.Is(err, store.ErrRequestConflict):
		return w.finishConflict(folder, path)
	default:
		if !w.bumpRetry(path) {
			w.log.Warn("ipc: persist request rejection", "folder", folder, "file", filepath.Base(path), "err", err)
			return false
		}
		w.log.Error("ipc: rejection tombstone not stored; failing without it", "folder", folder, "file", filepath.Base(path), "err", err)
	}
	return w.finishRejected(folder, path, code)
}

// rejectionCode extracts the persisted reason from a replayed rejection.
func rejectionCode(err error) string {
	msg := err.Error()
	if i := strings.LastIndex(msg, ": "); i >= 0 && i+2 < len(msg) {
		return sanitizeCode(msg[i+2:])
	}
	return codeInvalidRequest
}

func sanitizeCode(s string) string {
	for _, r := range s {
		if !(r == '_' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9') {
			return codeInvalidRequest
		}
	}
	return s
}

func (w *Watcher) finishRejected(folder, path, code string) bool {
	if !w.writeDurableReceipt(folder, path, durableReceipt{Status: "error", Error: code}) {
		return w.terminalBackoff(path)
	}
	return w.archiveTerminal(folder, path, ".failed")
}

// finishConflict records that reused filename carried different bytes. The
// original receipt keeps describing the original delivery; the conflict has its
// own receipt name so an agent cannot mistake it for that delivery.
func (w *Watcher) finishConflict(folder, path string) bool {
	if !w.writeDurableReceiptAs(folder, conflictReceiptName(path), durableReceipt{RequestFile: filepath.Base(path), Status: "error", Error: codeRequestConflict,
		Help: "This filename was already used for different bytes and was not sent. Use a new unique filename."}) {
		return w.terminalBackoff(path)
	}
	return w.archiveTerminal(folder, path, ".failed")
}

func (w *Watcher) finishQueued(ctx context.Context, folder, path string, delivery store.Delivery) bool {
	rc := durableReceiptFor(delivery.State, "")
	rc.Duplicate = !delivery.Created
	if diag, err := w.cfg.Durable.Store.DeliveryDiagnostic(ctx, delivery.ID); err == nil {
		rc.Detail = diag.Summary()
	}
	if !w.writeDurableReceipt(folder, path, rc) {
		return w.terminalBackoff(path)
	}
	w.rememberReceipt(folder, filepath.Base(path), delivery.State)
	return w.archiveTerminal(folder, path, ".processed")
}

func (w *Watcher) archiveTerminal(folder, path, destination string) bool {
	if err := w.archiveFile(folder, path, destination); err != nil {
		w.log.Error("ipc: archive durable request; it will converge on the same delivery", "folder", folder, "file", filepath.Base(path), "err", err)
		return w.terminalBackoff(path)
	}
	delete(w.retries, path)
	return true
}

// terminalBackoff throttles a request whose receipt or archive could not be
// written. The store state is already final, so nothing is tombstoned or
// failed: the file is retried on the normal backoff, then at the slowest
// cadence instead of every poll. It always returns false (not completed).
func (w *Watcher) terminalBackoff(path string) bool {
	if w.bumpRetry(path) {
		last := w.cfg.RetryBackoff[len(w.cfg.RetryBackoff)-1]
		w.retries[path] = retryEntry{attempts: w.cfg.MaxRetries, nextRetryAt: w.cfg.NowFn().Add(last)}
	}
	return false
}

// alreadyAccepted reports a committed delivery for this request key, so a file
// that lingered past expiry converges on its delivery instead of being rejected.
func (w *Watcher) alreadyAccepted(ctx context.Context, src store.ScopeRef, key string) bool {
	_, ok, err := w.cfg.Durable.Store.LookupIPCRequest(ctx, src, key)
	return err == nil && ok
}

// requestExpired rejects a request that waited in the channel longer than the
// delivery would live, so a long outage cannot release stale messages.
func (w *Watcher) requestExpired(folder, path string) bool {
	root, err := w.openNamespace(folder)
	if err != nil {
		return false
	}
	defer root.Close()
	info, err := root.Lstat(filepath.Join("messages", filepath.Base(path)))
	if err != nil {
		return false
	}
	return w.cfg.NowFn().Sub(info.ModTime()) > store.DefaultDeliveryExpiry
}
