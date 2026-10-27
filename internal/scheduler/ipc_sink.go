package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

// IPCSink adapts internal/store + scheduler validation to the
// ipc.TaskSink interface.
type IPCSink struct {
	st *store.Store
}

// NewIPCSink returns a fresh adapter. Callers wire it into the IPC
// watcher in cmd/gopod/main.go.
func NewIPCSink(st *store.Store) *IPCSink { return &IPCSink{st: st} }

// Schedule validates the schedule expression and inserts the record.
// Validation errors are wrapped with ipc.ErrPermanent so the watcher
// stops retrying.
func (a *IPCSink) Schedule(ctx context.Context, rec store.TaskRecord) error {
	release := taskOperationLock(ctx, a.st)
	defer release()
	if rec.Prompt == "" {
		return fmt.Errorf("prompt is required: %w", ipc.ErrPermanent)
	}
	if rec.ID != "" {
		existing, err := a.st.GetTask(ctx, rec.ID)
		if err == nil {
			if existing.ChatFolder == rec.ChatFolder && existing.ChatJID == rec.ChatJID && existing.Prompt == rec.Prompt && existing.ScheduleType == rec.ScheduleType && existing.ScheduleValue == rec.ScheduleValue {
				return nil
			}
			return fmt.Errorf("taskId already exists with different definition; use update: %w", ipc.ErrPermanent)
		}
		if !errors.Is(err, store.ErrTaskNotFound) {
			return err
		}
	}
	now := time.Now()
	if rec.CreatedAt > 0 {
		now = time.UnixMilli(rec.CreatedAt)
	}
	next, err := FirstRunAt(rec.ScheduleType, rec.ScheduleValue, now)
	if err != nil {
		return fmt.Errorf("invalid schedule: %w: %w", err, ipc.ErrPermanent)
	}
	rec.NextRun = next

	if _, err := a.st.CreateTask(ctx, rec); err != nil {
		return err
	}
	return nil
}

// Cancel marks an active task as "done". Unknown IDs are permanent
// failures so the IPC watcher does not retry.
func (a *IPCSink) Cancel(ctx context.Context, id string) error {
	release := taskOperationLock(ctx, a.st)
	defer release()
	t, err := a.st.GetTask(ctx, id)
	scope, scoped := ipc.TaskScopeFromContext(ctx)
	if scoped && !scope.IsOwner && (errors.Is(err, store.ErrTaskNotFound) || err == nil && t.ChatFolder != scope.SourceFolder) {
		return fmt.Errorf("task unavailable in this scope: %w", ipc.ErrPermanent)
	}
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			return fmt.Errorf("cancel %q: %w: %w", id, err, ipc.ErrPermanent)
		}
		return err
	}
	if scope, ok := ipc.TaskScopeFromContext(ctx); ok && !scope.IsOwner && t.ChatFolder != scope.SourceFolder {
		return fmt.Errorf("task unavailable in this scope: %w", ipc.ErrPermanent)
	}
	if t.Status == "done" {
		return nil
	}
	return a.st.UpdateTaskStatus(ctx, id, "done", 0, t.LastRun)
}
