package scheduler

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

func (a *IPCSink) Manage(ctx context.Context, op string, input store.TaskRecord) (ipc.TaskResult, error) {
	release := taskOperationLock(ctx, a.st)
	defer release()
	scope, ok := ipc.TaskScopeFromContext(ctx)
	if !ok {
		return ipc.TaskResult{}, fmt.Errorf("missing IPC source scope: %w", ipc.ErrPermanent)
	}
	fail := func(message string) (ipc.TaskResult, error) {
		return ipc.TaskResult{}, fmt.Errorf("%s: %w", message, ipc.ErrPermanent)
	}
	if op == "list" {
		if input.ChatFolder == "" {
			input.ChatFolder = scope.SourceFolder
		}
		if !scope.IsOwner && input.ChatFolder != scope.SourceFolder {
			return fail("task list unavailable in this scope")
		}
		tasks, err := a.st.ListTasksByChat(ctx, input.ChatFolder)
		if err != nil {
			return ipc.TaskResult{}, err
		}
		out := ipc.TaskResult{Tasks: make([]ipc.TaskInfo, 0, len(tasks))}
		for _, t := range tasks {
			out.Tasks = append(out.Tasks, ipc.DescribeTask(t))
		}
		return out, nil
	}
	if input.ID == "" {
		return fail("taskId is required")
	}
	t, err := a.st.GetTask(ctx, input.ID)
	if err != nil {
		return fail("task unavailable in this scope")
	}
	if !scope.IsOwner && t.ChatFolder != scope.SourceFolder {
		return fail("task unavailable in this scope")
	}
	result := ipc.TaskResult{TaskID: t.ID}
	switch op {
	case "get":
	case "pause":
		if t.Status != "done" {
			err = a.st.UpdateTaskStatus(ctx, t.ID, "paused", 0, t.LastRun)
		}
	case "resume":
		if t.Status == "done" {
			return fail("completed task cannot resume; schedule a new task")
		}
		if t.Status == "paused" {
			next, e := FirstRunAt(t.ScheduleType, t.ScheduleValue, time.Now())
			if e != nil {
				return fail(e.Error())
			}
			err = a.st.UpdateTaskStatus(ctx, t.ID, "active", next, t.LastRun)
		}
	case "update":
		if t.Status == "done" {
			return fail("completed task cannot update; schedule a new task")
		}
		if input.Prompt != "" {
			t.Prompt = input.Prompt
		}
		if input.ScheduleType != "" || input.ScheduleValue != "" {
			if input.ScheduleType != "" {
				t.ScheduleType = input.ScheduleType
			}
			if input.ScheduleValue != "" {
				t.ScheduleValue = input.ScheduleValue
			}
			next, e := FirstRunAt(t.ScheduleType, t.ScheduleValue, time.Now())
			if e != nil {
				return fail(e.Error())
			}
			t.NextRun = next
			if t.Status == "paused" {
				t.NextRun = 0
			}
		}
		err = a.st.UpdateTaskDefinition(ctx, t)
		if errors.Is(err, store.ErrManagedTask) {
			return fail("managed tasks cannot be changed by this command")
		}
	case "delete":
		err = a.st.DeleteTask(ctx, t.ID)
		if errors.Is(err, store.ErrManagedTask) {
			return fail("managed tasks cannot be deleted by this command")
		}
		result.Deleted = err == nil
		return result, err
	default:
		return fail("unsupported task operation")
	}
	if err != nil {
		return ipc.TaskResult{}, err
	}
	t, err = a.st.GetTask(ctx, t.ID)
	if err != nil {
		return ipc.TaskResult{}, err
	}
	info := ipc.DescribeTask(t)
	result.Task = &info
	return result, nil
}
