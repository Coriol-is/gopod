package ipc

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

func TestWatcher_TaskScheduleHappyPath(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "tasks", "0001.json", map[string]any{
		"op":            "schedule",
		"taskId":        "task-fixed-1",
		"prompt":        "remind me",
		"scheduleType":  "cron",
		"scheduleValue": "0 9 * * *",
	})
	tasks := &fakeTaskSink{}
	w := New(Config{
		DataDir: root, OwnerFolder: "owner",
		NowFn: func() time.Time { return time.Unix(1_700_000_000, 0) },
	}, &fakeMessageSink{}, tasks, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if got := len(tasks.scheduled); got != 1 {
		t.Fatalf("scheduled = %d, want 1", got)
	}
	rec := tasks.scheduled[0]
	if rec.ID != "task-fixed-1" {
		t.Errorf("ID = %q, want task-fixed-1", rec.ID)
	}
	if rec.ChatFolder != "alice" {
		t.Errorf("ChatFolder = %q, want alice", rec.ChatFolder)
	}
	if rec.Prompt != "remind me" {
		t.Errorf("Prompt = %q", rec.Prompt)
	}
	if rec.ScheduleType != "cron" || rec.ScheduleValue != "0 9 * * *" {
		t.Errorf("schedule = %+v", rec)
	}
	if rec.Status != "active" {
		t.Errorf("Status = %q, want active", rec.Status)
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".processed")); len(got) != 1 {
		t.Errorf(".processed = %v, want 1", got)
	}
}

func TestWatcher_TaskScheduleGeneratesIDWhenAbsent(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "tasks", "0001.json", map[string]any{
		"op":            "schedule",
		"prompt":        "remind",
		"scheduleType":  "interval",
		"scheduleValue": "1h",
	})
	tasks := &fakeTaskSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner",
		NowFn: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		&fakeMessageSink{}, tasks, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if len(tasks.scheduled) != 1 {
		t.Fatalf("scheduled = %d, want 1", len(tasks.scheduled))
	}
	if tasks.scheduled[0].ID == "" {
		t.Error("generated ID empty")
	}
}

func TestWatcher_TaskCancelHappyPath(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "tasks", "0001.json", map[string]any{
		"op":     "cancel",
		"taskId": "task-fixed-1",
	})
	tasks := &fakeTaskSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		&fakeMessageSink{}, tasks, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if len(tasks.cancelled) != 1 || tasks.cancelled[0] != "task-fixed-1" {
		t.Errorf("cancelled = %v", tasks.cancelled)
	}
}

func TestWatcher_TaskUnknownOp(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "tasks", "0001.json", map[string]any{
		"op": "frobnicate",
	})
	tasks := &fakeTaskSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		&fakeMessageSink{}, tasks, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if len(tasks.scheduled)+len(tasks.cancelled) != 0 {
		t.Errorf("sink called for unknown op")
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}

func TestWatcher_TaskCrossFolderNonOwnerBlocked(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "tasks", "0001.json", map[string]any{
		"op":               "schedule",
		"prompt":           "x",
		"scheduleType":     "once",
		"scheduleValue":    "2030-01-01T00:00:00Z",
		"targetChatFolder": "owner",
	})
	tasks := &fakeTaskSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner",
		NowFn: func() time.Time { return time.Unix(1_700_000_000, 0) }},
		&fakeMessageSink{}, tasks, nil,
		staticResolver(map[string]string{"alice": "2002", "owner": "1001"}))
	w.tickOnce(context.Background())
	if len(tasks.scheduled) != 0 {
		t.Errorf("non-owner cross-folder schedule went through")
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}
