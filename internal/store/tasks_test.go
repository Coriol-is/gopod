package store

import (
	"context"
	"errors"
	"testing"
)

func TestCreateAndGetTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.CreateTask(ctx, TaskRecord{
		ChatFolder:    "owner",
		ChatJID:       "tg:123",
		Prompt:        "check weather",
		ScheduleType:  "cron",
		ScheduleValue: "0 9 * * *",
		NextRun:       1700000000000,
		Status:        "active",
	})
	if err != nil {
		t.Fatalf("CreateTask: %v", err)
	}
	if id == "" {
		t.Fatal("empty ID returned")
	}

	got, err := s.GetTask(ctx, id)
	if err != nil {
		t.Fatalf("GetTask: %v", err)
	}
	if got.Prompt != "check weather" || got.ScheduleType != "cron" {
		t.Errorf("got %+v", got)
	}
}

func TestGetTaskNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.GetTask(ctx, "nonexistent")
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("err = %v, want ErrTaskNotFound", err)
	}
}

func TestListTasksByChat(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "a", ScheduleType: "cron", ScheduleValue: "* * * * *", NextRun: 100, CreatedAt: 1})
	s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "b", ScheduleType: "once", ScheduleValue: "2026-01-01", NextRun: 200, CreatedAt: 2})
	s.CreateTask(ctx, TaskRecord{ChatFolder: "alice", ChatJID: "tg:2", Prompt: "c", ScheduleType: "interval", ScheduleValue: "1h", NextRun: 300, CreatedAt: 3})

	tasks, err := s.ListTasksByChat(ctx, "owner")
	if err != nil {
		t.Fatalf("ListTasksByChat: %v", err)
	}
	if len(tasks) != 2 {
		t.Errorf("len = %d, want 2", len(tasks))
	}
}

func TestGetDueTasks(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "due", ScheduleType: "cron", ScheduleValue: "* * * * *", NextRun: 100, Status: "active"})
	s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "future", ScheduleType: "cron", ScheduleValue: "* * * * *", NextRun: 99999, Status: "active"})
	s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "paused", ScheduleType: "cron", ScheduleValue: "* * * * *", NextRun: 50, Status: "paused"})

	due, err := s.GetDueTasks(ctx, 500)
	if err != nil {
		t.Fatalf("GetDueTasks: %v", err)
	}
	if len(due) != 1 || due[0].Prompt != "due" {
		t.Errorf("due = %+v, want [due]", due)
	}
}

func TestUpdateTaskStatus(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, _ := s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "x", ScheduleType: "once", ScheduleValue: "now", NextRun: 1, Status: "active"})

	if err := s.UpdateTaskStatus(ctx, id, "paused", 0, 1000); err != nil {
		t.Fatalf("UpdateTaskStatus: %v", err)
	}
	got, _ := s.GetTask(ctx, id)
	if got.Status != "paused" || got.LastRun != 1000 {
		t.Errorf("got %+v", got)
	}
}

func TestLogTaskRun(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.LogTaskRun(ctx, TaskRunLog{
		TaskID:     "task-1",
		RunAt:      1700000000000,
		DurationMs: 1500,
		Status:     "success",
		Result:     "weather is sunny",
	}); err != nil {
		t.Fatalf("LogTaskRun: %v", err)
	}
}

func TestDeleteTask(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, _ := s.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "x", ScheduleType: "once", ScheduleValue: "now", NextRun: 1})
	if err := s.DeleteTask(ctx, id); err != nil {
		t.Fatalf("DeleteTask: %v", err)
	}
	_, err := s.GetTask(ctx, id)
	if !errors.Is(err, ErrTaskNotFound) {
		t.Errorf("after delete, GetTask: %v, want ErrTaskNotFound", err)
	}
}

func TestTaskRunLifecycle(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.LogTaskRun(ctx, TaskRunLog{TaskID: "t1", RunAt: 1000, Status: "running"}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTaskRunTurn(ctx, "t1", 1000, 55); err != nil {
		t.Fatal(err)
	}
	if err := s.FinishTaskRun(ctx, "t1", 1000, "success", 1234, ""); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTaskRun(ctx, "t1", 1000)
	if err != nil {
		t.Fatal(err)
	}
	if got.TurnID != 55 || got.Status != "success" || got.DurationMs != 1234 {
		t.Errorf("run = %+v", got)
	}
	if err := s.FinishTaskRun(ctx, "nope", 1, "error", 0, "x"); !errors.Is(err, ErrTaskRunNotFound) {
		t.Errorf("unknown run err = %v, want ErrTaskRunNotFound", err)
	}
}
