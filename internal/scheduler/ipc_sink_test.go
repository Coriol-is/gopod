package scheduler

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

func openTestStore(t *testing.T) *store.Store {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), log)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func TestIPCSink_Schedule_PersistsTask(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "2002", Folder: "alice", AddedAt: 1700000000000}); err != nil {
		t.Fatalf("RegisterChat: %v", err)
	}

	sink := NewIPCSink(st)
	rec := store.TaskRecord{
		ID:            "task-1",
		ChatFolder:    "alice",
		ChatJID:       "2002",
		Prompt:        "ping",
		ScheduleType:  "cron",
		ScheduleValue: "0 9 * * *",
		Status:        "active",
	}
	if err := sink.Schedule(ctx, rec); err != nil {
		t.Fatalf("Schedule: %v", err)
	}
	got, err := st.GetTask(ctx, "task-1")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Prompt != "ping" {
		t.Errorf("Prompt = %q, want ping", got.Prompt)
	}
}

func TestIPCSink_Schedule_BadCronIsPermanent(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	st.RegisterChat(ctx, store.RegisteredChat{JID: "2002", Folder: "alice", AddedAt: 1700000000000})

	sink := NewIPCSink(st)
	rec := store.TaskRecord{
		ID:            "task-2",
		ChatFolder:    "alice",
		ChatJID:       "2002",
		ScheduleType:  "cron",
		ScheduleValue: "not a cron",
	}
	err := sink.Schedule(ctx, rec)
	if err == nil {
		t.Fatal("Schedule(bad cron): err = nil, want non-nil")
	}
	if !errors.Is(err, ipc.ErrPermanent) {
		t.Errorf("Schedule(bad cron): err = %v, want ipc.ErrPermanent", err)
	}
}

func TestIPCSink_Cancel_MarksDone(t *testing.T) {
	ctx := context.Background()
	st := openTestStore(t)
	st.RegisterChat(ctx, store.RegisteredChat{JID: "2002", Folder: "alice", AddedAt: 1700000000000})
	if _, err := st.CreateTask(ctx, store.TaskRecord{
		ID:            "task-c",
		ChatFolder:    "alice",
		ChatJID:       "2002",
		Prompt:        "ping",
		ScheduleType:  "interval",
		ScheduleValue: "1h",
		Status:        "active",
	}); err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	sink := NewIPCSink(st)
	if err := sink.Cancel(ctx, "task-c"); err != nil {
		t.Fatalf("Cancel: %v", err)
	}
	got, err := st.GetTask(ctx, "task-c")
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Status != "done" {
		t.Errorf("Status = %q, want done", got.Status)
	}
}

func TestIPCSink_Cancel_UnknownIDIsPermanent(t *testing.T) {
	st := openTestStore(t)
	sink := NewIPCSink(st)
	err := sink.Cancel(context.Background(), "nope")
	if err == nil || !errors.Is(err, ipc.ErrPermanent) {
		t.Errorf("Cancel(unknown): err = %v, want ipc.ErrPermanent", err)
	}
}
