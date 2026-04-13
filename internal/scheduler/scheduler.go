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
	"fmt"
	"log/slog"
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

// New creates a Scheduler.
func New(st *store.Store, q *queue.Queue, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{store: st, queue: q, log: log}
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
	start := time.Now()

	// Push the task's prompt through the queue as if it were a
	// Telegram message. The queue worker calls runner.Run which
	// does the actual claude -p invocation.
	s.queue.Enqueue(ctx, queue.Item{
		ChatID:  0, // no Telegram reply; tasks run silently for now
		Folder:  t.ChatFolder,
		IsOwner: false, // tasks run as non-owner tier (conservative)
		Text:    t.Prompt,
	})

	duration := time.Since(start)

	// Log the run.
	runStatus := "success"
	if err := s.store.LogTaskRun(ctx, store.TaskRunLog{
		TaskID:     t.ID,
		RunAt:      now,
		DurationMs: duration.Milliseconds(),
		Status:     runStatus,
	}); err != nil {
		s.log.Error("scheduler: LogTaskRun", slog.String("task", t.ID), slog.Any("err", err))
	}

	// Compute next_run.
	nextRun, newStatus := computeNextRun(t, now)
	if err := s.store.UpdateTaskStatus(ctx, t.ID, newStatus, nextRun, now); err != nil {
		s.log.Error("scheduler: UpdateTaskStatus", slog.String("task", t.ID), slog.Any("err", err))
	}

	s.log.Info("scheduler: task executed",
		slog.String("task", t.ID),
		slog.String("type", t.ScheduleType),
		slog.String("folder", t.ChatFolder),
		slog.String("new_status", newStatus),
		slog.Int64("next_run", nextRun))
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
