package scheduler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestNonOwnerPeriodChecksWholeClock(t *testing.T) {
	for _, tc := range []struct {
		kind, value string
		allowed     bool
	}{
		{"interval", "1ms", false}, {"interval", "14m59s", false}, {"interval", "15m", true},
		{"cron", "* * * * *", false}, {"cron", "*/15 * * * *", true},
		{"cron", "0,59 * * * *", false}, {"cron", "0,5 0 1 1 *", false},
		{"cron", "CRON_TZ=Europe/Belgrade 0 9 * * *", true},
	} {
		if err := ValidateNonOwnerPeriod(tc.kind, tc.value); (err == nil) != tc.allowed {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestTaskCapsSerializeAndOwnerIsUnrestricted(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	for _, c := range []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:2", Folder: "alice", AddedAt: 1}} {
		if err := st.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	sink := NewIPCSink(st)
	var accepted, notices atomic.Int32
	SetTaskNotifier(st, func(context.Context, string) { notices.Add(1) })
	defer taskNotifiers.Delete(st)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if err := sink.Schedule(ctx, store.TaskRecord{ID: fmt.Sprint(i), ChatFolder: "alice", ChatJID: "tg:2", Prompt: "p", ScheduleType: "interval", ScheduleValue: "15m"}); err == nil {
				accepted.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if accepted.Load() != MaxNonOwnerTasks || notices.Load() != MaxNonOwnerTasks {
		t.Fatalf("accepted %d notices %d", accepted.Load(), notices.Load())
	}
	for i := 0; i < 20; i++ {
		if err := sink.Schedule(ctx, store.TaskRecord{ID: fmt.Sprintf("owner-%d", i), ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1ms"}); err != nil {
			t.Fatal(err)
		}
	}
	if notices.Load() != MaxNonOwnerTasks {
		t.Fatal("owner schedules notified")
	}
}

func TestDailyBudgetPersistsAndResetsAtUTCDate(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC).UnixMilli()
	for i := 0; i < MaxNonOwnerDailyRuns; i++ {
		allowed, err := reserveDailyRun(ctx, st, "alice", now)
		if err != nil || !allowed {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	// The state is in SQLite, not a Scheduler instance or a task row.
	if value, err := st.GetState(ctx, "task-budget:alice"); err != nil || value != "2026-01-01 96" {
		t.Fatalf("%q %v", value, err)
	}
	allowed, err := reserveDailyRun(ctx, st, "alice", now)
	if err != nil || allowed {
		t.Fatal("exhausted budget allowed")
	}
	allowed, err = reserveDailyRun(ctx, st, "alice", now+24*time.Hour.Milliseconds())
	if err != nil || !allowed {
		t.Fatal("new day stayed blocked")
	}
}

func TestRemovedRegistrationAndExhaustedBudgetNeverEnqueue(t *testing.T) {
	for _, mode := range []string{"missing", "mismatch", "exhausted", "unsafe-period"} {
		t.Run(mode, func(t *testing.T) {
			st := openTestStore(t)
			ctx := context.Background()
			now := time.Now().UnixMilli()
			if mode != "missing" {
				if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:2", Folder: "alice", AddedAt: 1}); err != nil {
					t.Fatal(err)
				}
			}
			rec := store.TaskRecord{ID: "t", ChatFolder: "alice", ChatJID: "tg:2", Prompt: "p", ScheduleType: "interval", ScheduleValue: "15m", NextRun: now - 1}
			if mode == "mismatch" {
				rec.ChatJID = "tg:3"
			}
			if mode == "unsafe-period" {
				rec.ScheduleValue = "1ms"
			}
			if _, err := st.CreateTask(ctx, rec); err != nil {
				t.Fatal(err)
			}
			if mode == "exhausted" {
				if err := st.SetState(ctx, "task-budget:alice", time.Now().UTC().Format("2006-01-02")+" 96"); err != nil {
					t.Fatal(err)
				}
			}
			var calls atomic.Int32
			q := queue.New(ctx, func(context.Context, []queue.Item) error { calls.Add(1); return nil }, 1, storeAdapter{st}, discardLogger())
			defer q.Close()
			s := New(st, q, discardLogger())
			s.runTask(ctx, rec, now)
			if calls.Load() != 0 || q.Pending() != 0 || q.ActiveChats() != 0 {
				t.Fatal("denied task enqueued")
			}
			if _, err := st.GetTaskRun(ctx, rec.ID, rec.NextRun); err != store.ErrTaskRunNotFound {
				t.Fatalf("created run: %v", err)
			}
		})
	}
}
