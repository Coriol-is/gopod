package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type messageOutcome struct{ cause error }

// Finish terminal outcomes without repeating the network send when archiving
// fails. This is process-local protection, not crash-safe exactly-once delivery.
func (w *Watcher) finishMessage(folder, path string, outcome messageOutcome) bool {
	status, destination := "ok", ".processed"
	if outcome.cause != nil {
		status, destination = "error", ".failed"
	}
	w.writeMessageReceipt(folder, path, status, outcome.cause)
	if err := w.archiveFile(folder, path, destination); err != nil {
		w.log.Error("ipc: archive terminal message; delivery will not be repeated in this process", "folder", folder, "file", filepath.Base(path), "err", err)
		return false
	}
	delete(w.terminalMessages, path)
	delete(w.retries, path)
	return true
}

type messagePayload struct {
	ChatJID string          `json:"chatJid"`
	Text    string          `json:"text"`
	Format  json.RawMessage `json:"format"`
	Buttons json.RawMessage `json:"buttons"`
	Sticker json.RawMessage `json:"sticker"`
}

// processMessageFile parses one messages/*.json and dispatches it.
// Return value is "completed" — true means the watcher should not
// touch this file again on later ticks (success or .failed/-moved).
// false means a retry is scheduled. The current safeDispatch / processChannel
// loop ignores this return value because the rename-out-of-messages/ is
// the ground truth; the bool is reserved for a future "skip remaining
// channel files this tick" optimisation.
func (w *Watcher) processMessageFile(ctx context.Context, folder, path string) (completed bool) {
	if outcome, ok := w.terminalMessages[path]; ok {
		return w.finishMessage(folder, path, outcome)
	}
	// Bound process-local quarantine state; preserve requests rather than sending
	// additional messages whose terminal outcome cannot be tracked.
	pending := 0
	for name := range w.terminalMessages {
		if filepath.Dir(name) != filepath.Dir(path) {
			continue
		}
		if _, err := os.Lstat(name); os.IsNotExist(err) {
			delete(w.terminalMessages, name)
			continue
		}
		pending++
	}
	if pending >= w.cfg.Retention {
		w.log.Error("ipc: message archive backlog full; repair archives before sending more", "folder", folder)
		return false
	}
	finish := func(cause error) bool {
		outcome := messageOutcome{cause: cause}
		w.terminalMessages[path] = outcome
		return w.finishMessage(folder, path, outcome)
	}
	fail := finish
	raw, err := w.readRequest(path, folder, "messages")
	if err != nil {
		return fail(fmt.Errorf("cannot read message: publish a regular non-symlink JSON file atomically"))
	}
	if len(raw) > 64<<10 {
		return fail(fmt.Errorf("message request exceeds 64 KiB"))
	}

	var p messagePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		return fail(fmt.Errorf("parse: %w", err))
	}
	if w.artifactsEnabled() && folder == w.cfg.OwnerFolder && isArtifactVersion(raw) {
		return w.processArtifactRequest(ctx, folder, path, raw)
	}
	if w.durableEligible(folder, p.ChatJID) {
		return w.processDurable(ctx, folder, path, raw, p)
	}
	if p.Format != nil || p.Buttons != nil || p.Sticker != nil {
		return w.finishRejected(folder, path, "rich_format_unavailable")
	}
	if err := authzMessage(folder, p.ChatJID, w.cfg.OwnerFolder, w.resolve); err != nil {
		return fail(err)
	}

	if p.Text == "" {
		return fail(fmt.Errorf("empty message text: %w", ErrPermanent))
	}
	err = w.msgs.Send(ctx, p.ChatJID, p.Text)
	if err == nil {
		return finish(nil)
	}
	if errors.Is(err, ErrPermanent) {
		return fail(err)
	}
	if w.bumpRetry(path) {
		return fail(fmt.Errorf("retries exhausted (delivery may be uncertain): %w", err))
	}
	w.writeMessageReceipt(folder, path, "retrying", fmt.Errorf("transient delivery error; host will retry"))
	w.log.Warn("ipc: message send transient error",
		"folder", folder, "channel", "messages", "file", filepath.Base(path),
		"attempts", w.retries[path].attempts, "err", err)
	return false
}
