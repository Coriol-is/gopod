package scheduler

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/robfig/cron/v3"
)

const MaxNonOwnerTasks = 10
const MaxNonOwnerDailyRuns = 96
const MinNonOwnerPeriod = 15 * time.Minute

var taskNotifiers sync.Map

// SetTaskNotifier installs the private owner notification callback at startup.
func SetTaskNotifier(st *store.Store, notify func(context.Context, string)) {
	taskNotifiers.Store(st, notify)
}

func NotifyTaskCreated(ctx context.Context, st *store.Store, t store.TaskRecord) {
	c, err := st.GetByFolder(ctx, t.ChatFolder)
	if err != nil || c.IsOwner {
		return
	}
	if f, ok := taskNotifiers.Load(st); ok {
		f.(func(context.Context, string))(ctx, fmt.Sprintf("A non-owner chat (%s) scheduled task %s (%s %s).", c.JID, t.ID, t.ScheduleType, t.ScheduleValue))
	}
}

// CheckTaskBudget is called under LockTasks for every creation, update and
// resume. The registration is host authority; request fields cannot exempt it.
func CheckTaskBudget(ctx context.Context, st *store.Store, t store.TaskRecord) error {
	c, err := st.GetByFolder(ctx, t.ChatFolder)
	if err != nil {
		return fmt.Errorf("task registration unavailable: %w", err)
	}
	if c.JID != t.ChatJID {
		return fmt.Errorf("task registration mismatch")
	}
	if c.IsOwner {
		return nil
	}
	if err := ValidateNonOwnerPeriod(t.ScheduleType, t.ScheduleValue); err != nil {
		return err
	}
	tasks, err := st.ListTasksByChat(ctx, t.ChatFolder)
	if err != nil {
		return err
	}
	count := 0
	for _, existing := range tasks {
		if existing.ID != t.ID && existing.Status != "done" {
			count++
		}
	}
	if count >= MaxNonOwnerTasks {
		return fmt.Errorf("non-owner chats may keep at most %d unfinished tasks", MaxNonOwnerTasks)
	}
	return nil
}

// ValidateNonOwnerPeriod conservatively checks the complete daily cron clock,
// including midnight, rather than sampling a few future occurrences. Date and
// weekday restrictions can only remove occurrences from this clock.
func ValidateNonOwnerPeriod(kind, value string) error {
	switch kind {
	case "interval":
		d, err := time.ParseDuration(value)
		if err != nil {
			return err
		}
		if d < MinNonOwnerPeriod {
			return fmt.Errorf("non-owner task period must be at least 15 minutes")
		}
	case "cron":
		sched, err := cronParser.Parse(value)
		if err != nil {
			return err
		}
		spec, ok := sched.(*cron.SpecSchedule)
		if !ok {
			return fmt.Errorf("unsupported non-owner cron schedule")
		}
		var slots []int
		for h := 0; h < 24; h++ {
			for m := 0; m < 60; m++ {
				if spec.Hour&(uint64(1)<<h) != 0 && spec.Minute&(uint64(1)<<m) != 0 {
					slots = append(slots, h*60+m)
				}
			}
		}
		for i, minute := range slots {
			next := slots[(i+1)%len(slots)]
			if i == len(slots)-1 {
				next += 24 * 60
			}
			if next-minute < 15 {
				return fmt.Errorf("non-owner cron occurrences must be at least 15 minutes apart")
			}
		}
	case "once":
	default:
		return fmt.Errorf("unknown schedule type")
	}
	return nil
}

// reserveDailyRun charges before enqueue, including failures. The durable
// folder counter survives restarts and task deletion/recreation. Caller holds
// LockTasks. Exhaustion delays the task until the next UTC day.
func reserveDailyRun(ctx context.Context, st *store.Store, folder string, now int64) (bool, error) {
	key := "task-budget:" + folder
	value, err := st.GetState(ctx, key)
	if err != nil {
		return false, err
	}
	day := time.UnixMilli(now).UTC().Format("2006-01-02")
	var oldDay string
	var count int
	if value != "" {
		if _, err := fmt.Sscanf(value, "%s %d", &oldDay, &count); err != nil {
			return false, fmt.Errorf("invalid task budget: %w", err)
		}
	}
	if oldDay != day {
		count = 0
	}
	if count >= MaxNonOwnerDailyRuns {
		return false, nil
	}
	if err := st.SetState(ctx, key, fmt.Sprintf("%s %d", day, count+1)); err != nil {
		return false, err
	}
	return true, nil
}
