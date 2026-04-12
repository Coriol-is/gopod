package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/spaceinvaderz/picoclaw/internal/scheduler"
	"github.com/spaceinvaderz/picoclaw/internal/store"
)

// RegisterTaskCommands adds the /tasks subcommands to the Router.
// All are ChatLocal — any registered chat can manage its own tasks,
// scoped by chat folder.
//
// Usage from Telegram:
//
//	/tasks                     — list tasks for this chat
//	/tasks add cron "0 9 * * *" check weather
//	/tasks add interval 1h remind me to stretch
//	/tasks add once "2026-12-25T09:00:00Z" merry christmas
//	/tasks pause <id>
//	/tasks resume <id>
//	/tasks cancel <id>
func RegisterTaskCommands(r *Router, st *store.Store, chatFolderLookup func(chatID int64) string) {
	r.Register("tasks", "tasks", "manage scheduled tasks (try: /tasks add)", PermChatLocal,
		tasksDispatch(st, chatFolderLookup))
}

func tasksDispatch(st *store.Store, lookup func(int64) string) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		folder := lookup(cmd.Caller.ChatID)
		if folder == "" {
			return Response{Text: "This chat is not registered.", Code: 1}, nil
		}

		if len(cmd.Args) == 0 {
			return tasksList(ctx, st, folder)
		}

		sub := cmd.Args[0]
		args := cmd.Args[1:]

		switch sub {
		case "list":
			return tasksList(ctx, st, folder)
		case "add":
			return tasksAdd(ctx, st, folder, cmd.Caller, args)
		case "pause":
			return tasksSetStatus(ctx, st, folder, args, "paused")
		case "resume":
			return tasksSetStatus(ctx, st, folder, args, "active")
		case "cancel":
			return tasksCancel(ctx, st, folder, args)
		default:
			return Response{
				Text: fmt.Sprintf("Unknown subcommand %q. Try: /tasks list, /tasks add, /tasks pause, /tasks resume, /tasks cancel", sub),
				Code: 1,
			}, nil
		}
	}
}

func tasksList(ctx context.Context, st *store.Store, folder string) (Response, error) {
	tasks, err := st.ListTasksByChat(ctx, folder)
	if err != nil {
		return Response{}, err
	}
	if len(tasks) == 0 {
		return Response{Text: "No scheduled tasks for this chat."}, nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Tasks for %s:\n\n", folder))
	for _, t := range tasks {
		nextStr := "—"
		if t.NextRun > 0 {
			nextStr = time.UnixMilli(t.NextRun).Format("2006-01-02 15:04")
		}
		fmt.Fprintf(&sb, "[%s] %s %s %s\n  prompt: %s\n  next: %s\n\n",
			t.Status, t.ID[:8], t.ScheduleType, t.ScheduleValue,
			truncate(t.Prompt, 60), nextStr)
	}
	return Response{Text: sb.String()}, nil
}

func tasksAdd(ctx context.Context, st *store.Store, folder string, caller Caller, args []string) (Response, error) {
	// /tasks add <type> <schedule> <prompt...>
	if len(args) < 3 {
		return Response{
			Text: "Usage: /tasks add <type> <schedule> <prompt>\n\n" +
				"Types:\n" +
				"  cron \"0 9 * * *\"       — run at 09:00 daily\n" +
				"  interval 1h            — run every hour\n" +
				"  once 2026-12-25T09:00  — run once at that time\n",
			Code: 1,
		}, nil
	}

	schedType := args[0]
	schedValue := args[1]
	prompt := strings.Join(args[2:], " ")

	var nextRun int64
	now := time.Now()

	switch schedType {
	case "cron":
		if err := scheduler.ValidateCron(schedValue); err != nil {
			return Response{Text: err.Error(), Code: 1}, nil
		}
		// Compute first next_run from now.
		// Re-parse to get the schedule (ValidateCron already verified).
		sched, _ := cronParserForAdd().Parse(schedValue)
		nextRun = sched.Next(now).UnixMilli()

	case "interval":
		if err := scheduler.ValidateInterval(schedValue); err != nil {
			return Response{Text: err.Error(), Code: 1}, nil
		}
		d, _ := time.ParseDuration(schedValue)
		nextRun = now.Add(d).UnixMilli()

	case "once":
		t, err := time.Parse(time.RFC3339, schedValue)
		if err != nil {
			// Try shorter form.
			t, err = time.Parse("2006-01-02T15:04", schedValue)
			if err != nil {
				return Response{
					Text: fmt.Sprintf("Cannot parse time %q. Use RFC3339 (2026-12-25T09:00:00Z) or 2026-12-25T09:00", schedValue),
					Code: 1,
				}, nil
			}
		}
		if t.Before(now) {
			return Response{Text: "Scheduled time is in the past.", Code: 1}, nil
		}
		nextRun = t.UnixMilli()

	default:
		return Response{
			Text: fmt.Sprintf("Unknown schedule type %q. Use: cron, interval, once", schedType),
			Code: 1,
		}, nil
	}

	chatJID := fmt.Sprintf("tg:%d", caller.ChatID)
	id, err := st.CreateTask(ctx, store.TaskRecord{
		ChatFolder:    folder,
		ChatJID:       chatJID,
		Prompt:        prompt,
		ScheduleType:  schedType,
		ScheduleValue: schedValue,
		NextRun:       nextRun,
		Status:        "active",
	})
	if err != nil {
		return Response{}, err
	}

	nextStr := time.UnixMilli(nextRun).Format("2006-01-02 15:04")
	return Response{
		Text: fmt.Sprintf("Task created: %s\nType: %s %s\nNext run: %s\nPrompt: %s",
			id[:8], schedType, schedValue, nextStr, truncate(prompt, 80)),
	}, nil
}

func tasksSetStatus(ctx context.Context, st *store.Store, folder string, args []string, status string) (Response, error) {
	if len(args) == 0 {
		return Response{Text: "Usage: /tasks " + status + " <task_id>", Code: 1}, nil
	}
	id := args[0]

	// Verify task belongs to this chat.
	t, err := st.GetTask(ctx, id)
	if err != nil {
		return Response{Text: "Task not found. Use full ID from /tasks list.", Code: 1}, nil
	}
	if t.ChatFolder != folder {
		return Response{Text: "Task belongs to a different chat.", Code: 1}, nil
	}

	nextRun := t.NextRun
	if status == "active" && t.Status == "paused" {
		// Resuming: recompute next_run from now.
		now := time.Now().UnixMilli()
		nextRun, _ = computeNextRunForResume(t, now)
	}
	if status == "paused" {
		nextRun = 0 // paused tasks don't fire
	}

	if err := st.UpdateTaskStatus(ctx, id, status, nextRun, t.LastRun); err != nil {
		return Response{}, err
	}
	return Response{Text: fmt.Sprintf("Task %s → %s", id[:8], status)}, nil
}

func tasksCancel(ctx context.Context, st *store.Store, folder string, args []string) (Response, error) {
	if len(args) == 0 {
		return Response{Text: "Usage: /tasks cancel <task_id>", Code: 1}, nil
	}
	id := args[0]

	t, err := st.GetTask(ctx, id)
	if err != nil {
		return Response{Text: "Task not found.", Code: 1}, nil
	}
	if t.ChatFolder != folder {
		return Response{Text: "Task belongs to a different chat.", Code: 1}, nil
	}

	if err := st.DeleteTask(ctx, id); err != nil {
		return Response{}, err
	}
	return Response{Text: fmt.Sprintf("Task %s cancelled and deleted.", id[:8])}, nil
}

// computeNextRunForResume recomputes next_run when a paused task is
// resumed. For cron: next occurrence from now. For interval: now + interval.
// For once: if the original time is in the past, keep it (will fire immediately).
func computeNextRunForResume(t store.TaskRecord, now int64) (int64, string) {
	switch t.ScheduleType {
	case "cron":
		sched, err := cronParserForAdd().Parse(t.ScheduleValue)
		if err != nil {
			return 0, "done"
		}
		return sched.Next(time.UnixMilli(now)).UnixMilli(), "active"
	case "interval":
		d, err := time.ParseDuration(t.ScheduleValue)
		if err != nil {
			return 0, "done"
		}
		return now + d.Milliseconds(), "active"
	case "once":
		return t.NextRun, "active" // keep original time
	default:
		return 0, "done"
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// cronParserForAdd returns the same parser the scheduler uses.
// Duplicated here to avoid importing internal/scheduler into control
// (would create a cycle since scheduler imports store which is at
// the same level). The parser config MUST match scheduler.cronParser.
func cronParserForAdd() cron.Parser {
	return cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
}

// suppress unused import
var _ = cron.Parser{}
