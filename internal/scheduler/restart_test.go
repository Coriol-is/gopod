package scheduler

import (
	"context"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
	"path/filepath"
	"testing"
	"time"
)

func TestForceRestartSuppressionPreservesSchedulesAndNonOwnerTasks(t *testing.T) {
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	q := queue.New(t.Context(), func(context.Context, []queue.Item) error { t.Error("suppression ran task"); return nil }, 1, nil, nil)
	defer q.Close()
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:-2", Folder: "group", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	s := New(st, q, nil)
	now := time.Now().Add(-time.Minute).UnixMilli()
	owner, err := st.CreateTask(t.Context(), store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "test", ScheduleType: "interval", ScheduleValue: "1h", Status: "active", NextRun: now})
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.CreateTask(t.Context(), store.TaskRecord{ChatFolder: "group", ChatJID: "tg:-2", Prompt: "test", ScheduleType: "interval", ScheduleValue: "1h", Status: "active", NextRun: now})
	if err != nil {
		t.Fatal(err)
	}
	if err = st.BeginRestart(t.Context(), store.RestartRecord{ChatID: 1, MessageID: 10, Mode: "force"}); err != nil {
		t.Fatal(err)
	}
	if err = st.SetRestartPhase(t.Context(), 10, "requested", "restarting"); err != nil {
		t.Fatal(err)
	}
	if err = s.SuppressForcedRestart(t.Context()); err != nil {
		t.Fatal(err)
	}
	a, _ := st.GetTask(t.Context(), owner)
	b, _ := st.GetTask(t.Context(), group)
	if a.Status != "active" || a.NextRun <= time.Now().UnixMilli() || b.NextRun != now {
		t.Fatal(a, b)
	}
	run, err := st.GetTaskRun(t.Context(), owner, now)
	if err != nil || run.Status != "error" {
		t.Fatal(run, err)
	}
	if err = s.SuppressForcedRestart(t.Context()); err != nil {
		t.Fatal(err)
	}
	a2, _ := st.GetTask(t.Context(), owner)
	if a.NextRun != a2.NextRun {
		t.Fatal("retry moved future schedule")
	}
}
