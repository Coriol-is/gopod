package scheduler

import (
	"context"
	"fmt"
	"github.com/Coriol-is/gopod/internal/store"
	"time"
)

// SuppressForcedRestart advances only due legacy occurrences, retaining recurring
// definitions. It is idempotent across crash/retry and runs before normal polling.
func (s *Scheduler) SuppressForcedRestart(ctx context.Context) error {
	unlock := LockTasks(s.store)
	defer unlock()
	r, err := s.store.ReadRestart(ctx)
	if err != nil {
		return err
	}
	if r == nil || r.Mode != "force" {
		return nil
	}
	switch r.Phase {
	case "forcing", "blocked", "restarting":
	default:
		return nil
	}
	now := time.Now().UnixMilli()
	tasks, err := s.store.GetDueTasks(ctx, now)
	if err != nil {
		return err
	}
	for _, task := range tasks {
		if task.ChatJID != fmt.Sprintf("tg:%d", r.ChatID) {
			continue
		}
		if task.NextRun <= 0 {
			continue
		}
		if _, err = s.store.GetTaskRun(ctx, task.ID, task.NextRun); err == store.ErrTaskRunNotFound {
			if err = s.store.LogTaskRun(ctx, store.TaskRunLog{TaskID: task.ID, RunAt: task.NextRun, Status: "error", Error: "forced restart: occurrence suppressed"}); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		next, status := computeNextRun(task, now)
		if err = s.store.UpdateTaskStatus(ctx, task.ID, status, next, task.LastRun); err != nil {
			return err
		}
	}
	return s.store.RecordRestartSuppression(ctx, r.MessageID, now)
}
