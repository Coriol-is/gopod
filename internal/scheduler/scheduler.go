// Package scheduler runs gopod's scheduled tasks.
//
// The Scheduler polls the store every PollInterval for due tasks
// (active tasks whose next_run ≤ now), executes each one by pushing
// the task's prompt through the queue (same path as Telegram messages),
// computes the next_run, and logs the result. One Scheduler goroutine
// per gopod process.
//
// Schedule types:
//   - cron: standard 5-field cron expression, parsed by robfig/cron/v3
//   - interval: Go duration string (e.g. "1h", "30m"), next_run = now + interval
//   - once: runs once, then status set to "done"
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// PollInterval is how often the scheduler checks the store for due
// tasks. 60s matches NanoClaw's SCHEDULER_POLL_INTERVAL.
const PollInterval = 60 * time.Second

// cronParser is the standard 5-field cron parser (minute, hour, dom,
// month, dow). Reused across calls so we validate user-supplied
// expressions with the same parser the scheduler uses at runtime.
var cronParser = cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)

// Scheduler polls the store for due tasks and pushes them through
// the queue.
type Scheduler struct {
	store *store.Store
	queue *queue.Queue
	log   *slog.Logger
}

// New creates a Scheduler and registers its completion hook on q, so
// task_run_logs rows are finalized when the queue reports the turn
// done rather than when it was enqueued.
func New(st *store.Store, q *queue.Queue, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	s := &Scheduler{store: st, queue: q, log: log}
	q.OnDone(s.onTurnDone)
	return s
}

// onTurnDone finalizes the task_run_logs row for a task turn.
func (s *Scheduler) onTurnDone(it queue.Item, err error) {
	if it.Source != "task" {
		return
	}
	taskID, runAt, ok := parseTaskSourceID(it.SourceID)
	if !ok {
		s.log.Error("scheduler: bad task source id", slog.String("source_id", it.SourceID), slog.Int64("turn", it.ID))
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	status, errText := "success", ""
	if err != nil {
		status, errText = "error", err.Error()
	}
	var durationMs int64
	if it.ID != 0 {
		turn, terr := s.store.GetTurn(ctx, it.ID)
		switch {
		case terr != nil:
			s.log.Warn("scheduler: GetTurn for duration", slog.Int64("turn", it.ID), slog.Any("err", terr))
		case turn.StartedAt > 0 && turn.FinishedAt >= turn.StartedAt:
			durationMs = turn.FinishedAt - turn.StartedAt
		}
	}
	if ferr := s.store.FinishTaskRun(ctx, taskID, runAt, status, durationMs, errText); ferr != nil {
		s.log.Error("scheduler: FinishTaskRun", slog.String("task", taskID), slog.Any("err", ferr))
	}
}

// Start runs the poll loop. Blocks until ctx is cancelled.
func (s *Scheduler) Start(ctx context.Context) {
	s.log.Info("scheduler started", slog.Duration("poll_interval", PollInterval))

	// Run once immediately at startup to catch tasks that became due
	// while gopod was down.
	s.poll(ctx)

	t := time.NewTicker(PollInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("scheduler stopped")
			return
		case <-t.C:
			s.poll(ctx)
		}
	}
}

func (s *Scheduler) poll(ctx context.Context) {
	now := time.Now().UnixMilli()
	tasks, err := s.store.GetDueTasks(ctx, now)
	if err != nil {
		s.log.Error("scheduler: GetDueTasks", slog.Any("err", err))
		return
	}
	if len(tasks) == 0 {
		return
	}
	s.log.Debug("scheduler: due tasks found", slog.Int("count", len(tasks)))

	for _, t := range tasks {
		s.runTask(ctx, t, now)
	}
}

func (s *Scheduler) runTask(ctx context.Context, t store.TaskRecord, now int64) {
	// slot is the task's scheduled time. It is stable across polls and
	// crashes, unlike now, so it keys both the log row and the queue
	// dedup. GetDueTasks only returns tasks with next_run set.
	slot := t.NextRun

	// 1. Log the run as running before anything can go wrong. A row
	// already present for this slot belongs to a live or earlier turn;
	// leave it alone.
	if _, err := s.store.GetTaskRun(ctx, t.ID, slot); errors.Is(err, store.ErrTaskRunNotFound) {
		if err := s.store.LogTaskRun(ctx, store.TaskRunLog{TaskID: t.ID, RunAt: slot, Status: "running"}); err != nil {
			s.log.Error("scheduler: LogTaskRun", slog.String("task", t.ID), slog.Any("err", err))
		}
	} else if err != nil {
		s.log.Error("scheduler: GetTaskRun", slog.String("task", t.ID), slog.Any("err", err))
	}

	// 2. Push the prompt through the queue as if it were a Telegram
	// message. Source/SourceID make a re-fired slot a no-op.
	turnID, dup := s.queue.Enqueue(ctx, queue.Item{
		Source:   "task",
		SourceID: taskSourceID(t.ID, slot),
		ChatID:   0, // no Telegram reply; tasks run silently for now
		Folder:   t.ChatFolder,
		IsOwner:  false, // tasks run as non-owner tier (conservative)
		Text:     t.Prompt,
	})
	outcome := "task enqueued"
	switch {
	case dup:
		outcome = "task slot already enqueued"
		s.log.Warn("scheduler: slot already enqueued, advancing",
			slog.String("task", t.ID), slog.Int64("slot", slot))
	case turnID != 0:
		if err := s.store.SetTaskRunTurn(ctx, t.ID, slot, turnID); err != nil {
			s.log.Error("scheduler: SetTaskRunTurn", slog.String("task", t.ID), slog.Any("err", err))
		}
	default:
		// The queue could not persist the turn, so no completion hook
		// will ever finalize the log row: close it here. The slot is
		// still advanced below, as a failed run would be.
		outcome = "task enqueue failed"
		s.log.Error("scheduler: enqueue failed, slot skipped",
			slog.String("task", t.ID), slog.Int64("slot", slot))
		if err := s.store.FinishTaskRun(ctx, t.ID, slot, "error", 0, "enqueue failed"); err != nil {
			s.log.Error("scheduler: FinishTaskRun", slog.String("task", t.ID), slog.Any("err", err))
		}
	}

	// 3. Compute next_run so the poll loop does not re-fire this slot.
	nextRun, newStatus := computeNextRun(t, now)
	if err := s.store.UpdateTaskStatus(ctx, t.ID, newStatus, nextRun, now); err != nil {
		s.log.Error("scheduler: UpdateTaskStatus", slog.String("task", t.ID), slog.Any("err", err))
	}

	s.log.Info("scheduler: "+outcome,
		slog.String("task", t.ID),
		slog.String("type", t.ScheduleType),
		slog.String("folder", t.ChatFolder),
		slog.Int64("turn", turnID),
		slog.String("new_status", newStatus),
		slog.Int64("next_run", nextRun))
}

// taskSourceID is the queue dedup key for one scheduled slot.
func taskSourceID(taskID string, runAt int64) string {
	return taskID + ":" + strconv.FormatInt(runAt, 10)
}

// parseTaskSourceID inverts taskSourceID.
func parseTaskSourceID(s string) (string, int64, bool) {
	i := strings.LastIndexByte(s, ':')
	if i <= 0 || i == len(s)-1 {
		return "", 0, false
	}
	runAt, err := strconv.ParseInt(s[i+1:], 10, 64)
	if err != nil {
		return "", 0, false
	}
	return s[:i], runAt, true
}

// computeNextRun returns the next scheduled time and the new status
// for the task after it has been executed.
func computeNextRun(t store.TaskRecord, now int64) (nextRun int64, status string) {
	switch t.ScheduleType {
	case "cron":
		sched, err := cronParser.Parse(t.ScheduleValue)
		if err != nil {
			// Bad expression — shouldn't happen if validated at create
			// time, but don't crash.
			return 0, "done"
		}
		next := sched.Next(time.UnixMilli(now))
		return next.UnixMilli(), "active"

	case "interval":
		d, err := time.ParseDuration(t.ScheduleValue)
		if err != nil {
			return 0, "done"
		}
		return now + d.Milliseconds(), "active"

	case "once":
		return 0, "done"

	default:
		return 0, "done"
	}
}

// ValidateCron checks whether expr is a valid 5-field cron expression.
// Used by the /tasks add command to reject bad expressions before
// persisting.
func ValidateCron(expr string) error {
	_, err := cronParser.Parse(expr)
	if err != nil {
		return fmt.Errorf("invalid cron expression: %w", err)
	}
	return nil
}

// ValidateInterval checks whether s is a valid Go duration string.
func ValidateInterval(s string) error {
	_, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("invalid interval: %w", err)
	}
	return nil
}
