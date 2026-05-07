package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type messagePayload struct {
	ChatJID string `json:"chatJid"`
	Text    string `json:"text"`
}

// processMessageFile parses one messages/*.json and dispatches it.
// Return value is "completed" — true means the watcher should not
// touch this file again on later ticks (success or .failed/-moved).
// false means a retry is scheduled. The current safeDispatch / processChannel
// loop ignores this return value because the rename-out-of-messages/ is
// the ground truth; the bool is reserved for a future "skip remaining
// channel files this tick" optimisation.
func (w *Watcher) processMessageFile(ctx context.Context, folder, path string) (completed bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		w.log.Warn("ipc: read message file",
			"folder", folder, "file", filepath.Base(path), "err", err)
		return false
	}
	var p messagePayload
	if err := json.Unmarshal(raw, &p); err != nil {
		w.moveFailed(folder, path, fmt.Errorf("parse: %w", err))
		return true
	}
	if err := authzMessage(folder, p.ChatJID, w.cfg.OwnerFolder, w.resolve); err != nil {
		w.moveFailed(folder, path, err)
		return true
	}

	err = w.msgs.Send(ctx, p.ChatJID, p.Text)
	if err == nil {
		w.moveProcessed(folder, path)
		return true
	}
	if errors.Is(err, ErrPermanent) {
		w.moveFailed(folder, path, err)
		return true
	}
	if w.bumpRetry(path) {
		w.moveFailed(folder, path, fmt.Errorf("retries exhausted: %w", err))
		return true
	}
	w.log.Warn("ipc: message send transient error",
		"folder", folder, "channel", "messages", "file", filepath.Base(path),
		"attempts", w.retries[path].attempts, "err", err)
	return false
}
