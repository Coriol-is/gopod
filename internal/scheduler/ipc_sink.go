package scheduler

import (
	"context"
	"errors"
	"fmt"

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
	switch rec.ScheduleType {
	case "cron":
		if err := ValidateCron(rec.ScheduleValue); err != nil {
			return fmt.Errorf("validate cron %q: %w: %w",
				rec.ScheduleValue, err, ipc.ErrPermanent)
		}
	case "interval":
		if err := ValidateInterval(rec.ScheduleValue); err != nil {
			return fmt.Errorf("validate interval %q: %w: %w",
				rec.ScheduleValue, err, ipc.ErrPermanent)
		}
	case "once":
		if rec.ScheduleValue == "" {
			return fmt.Errorf("once: empty value: %w", ipc.ErrPermanent)
		}
	default:
		return fmt.Errorf("unknown scheduleType %q: %w",
			rec.ScheduleType, ipc.ErrPermanent)
	}
	if _, err := a.st.CreateTask(ctx, rec); err != nil {
		return err
	}
	return nil
}

// Cancel marks an active task as "done". Unknown IDs are permanent
// failures so the IPC watcher does not retry.
func (a *IPCSink) Cancel(ctx context.Context, id string) error {
	t, err := a.st.GetTask(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrTaskNotFound) {
			return fmt.Errorf("cancel %q: %w: %w", id, err, ipc.ErrPermanent)
		}
		return err
	}
	if t.Status == "done" {
		return nil
	}
	return a.st.UpdateTaskStatus(ctx, id, "done", 0, t.LastRun)
}
