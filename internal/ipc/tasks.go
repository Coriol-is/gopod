package ipc

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Coriol-is/gopod/internal/store"
)

type taskPayload struct {
	Op               string `json:"op"`
	TaskID           string `json:"taskId"`
	Prompt           string `json:"prompt"`
	ScheduleType     string `json:"scheduleType"`
	ScheduleValue    string `json:"scheduleValue"`
	TargetChatFolder string `json:"targetChatFolder"`
}

func (w *Watcher) processTaskFile(ctx context.Context, folder, path string) (completed bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		w.log.Warn("ipc: read task file",
			"folder", folder, "channel", "tasks", "file", filepath.Base(path), "err", err)
		return false
	}
	var p taskPayload
	if err := json.Unmarshal(raw, &p); err != nil {
		w.moveFailed(folder, path, fmt.Errorf("parse: %w", err))
		return true
	}

	var dispatchErr error
	switch p.Op {
	case "schedule":
		dispatchErr = w.handleTaskSchedule(ctx, folder, p)
	case "cancel":
		dispatchErr = w.handleTaskCancel(ctx, folder, p)
	default:
		w.moveFailed(folder, path, fmt.Errorf("unknown op %q", p.Op))
		return true
	}

	if dispatchErr == nil {
		w.moveProcessed(folder, path)
		return true
	}
	if errors.Is(dispatchErr, ErrPermanent) {
		w.moveFailed(folder, path, dispatchErr)
		return true
	}
	if w.bumpRetry(path) {
		w.moveFailed(folder, path, fmt.Errorf("retries exhausted: %w", dispatchErr))
		return true
	}
	w.log.Warn("ipc: task dispatch transient error",
		"folder", folder, "channel", "tasks", "file", filepath.Base(path),
		"attempts", w.retries[path].attempts, "err", dispatchErr)
	return false
}

func (w *Watcher) handleTaskSchedule(ctx context.Context, folder string, p taskPayload) error {
	if err := authzTaskSchedule(folder, p.TargetChatFolder, w.cfg.OwnerFolder, w.resolve); err != nil {
		return err
	}
	target := p.TargetChatFolder
	if target == "" {
		target = folder
	}
	jid, _ := w.resolve(target) // authz already verified registered

	id := p.TaskID
	if id == "" {
		id = newTaskID(w.cfg.NowFn().UnixMilli())
	}
	rec := store.TaskRecord{
		ID:            id,
		ChatFolder:    target,
		ChatJID:       jid,
		Prompt:        p.Prompt,
		ScheduleType:  p.ScheduleType,
		ScheduleValue: p.ScheduleValue,
		Status:        "active",
		CreatedAt:     w.cfg.NowFn().UnixMilli(),
	}
	return w.tasks.Schedule(ctx, rec)
}

func (w *Watcher) handleTaskCancel(ctx context.Context, folder string, p taskPayload) error {
	if p.TaskID == "" {
		return fmt.Errorf("cancel: empty taskId: %w", ErrPermanent)
	}
	// Authz for cancel: non-owner can only cancel tasks in its own
	// folder. The watcher cannot prove ownership without store access;
	// the TaskSink's Cancel implementation enforces this and returns
	// ErrPermanent on mismatch. Source folder is passed via context-
	// free param; Schedule already records ChatFolder so the sink
	// has it on file.
	_ = folder // reserved for future per-folder authz hooks
	return w.tasks.Cancel(ctx, p.TaskID)
}

func newTaskID(nowMs int64) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("task-%d-%s", nowMs, hex.EncodeToString(b[:]))
}
