package control

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

// PromptFunc is a callback that sends a prompt to Claude and returns
// the reply text. Used by tasksAdd to parse natural language schedules.
// Wired from main.go as a closure over runner.RunPrompt.
type PromptFunc func(ctx context.Context, folder string, prompt string) (string, error)

// RegisterTaskCommands adds the /tasks subcommands to the Router.
// All are ChatLocal — any registered chat can manage its own tasks,
// scoped by chat folder.
//
// Usage from Telegram (natural language):
//
//	/tasks                                — list tasks for this chat
//	/tasks add check weather every morning at 9
//	/tasks add remind me to stretch every hour
//	/tasks add say merry christmas on dec 25 at 9am
//	/tasks pause <id>
//	/tasks resume <id>
//	/tasks cancel <id>
//
// The "add" subcommand sends the user's text to Claude, which parses
// it into a structured {type, schedule, prompt} JSON. gopod then
// validates the parsed values before persisting. If promptFn is nil,
// falls back to the old manual syntax (cron/interval/once + value).
func RegisterTaskCommands(r *Router, st *store.Store, chatFolderLookup func(chatID int64) string, promptFn PromptFunc) {
	r.Register("tasks", "tasks", "manage scheduled tasks (try: /tasks add)", PermChatLocal,
		tasksDispatch(st, chatFolderLookup, promptFn))
	r.Register("tasks.state", "", "change a task state from its list", PermChatLocal, tasksStateButton(st, chatFolderLookup))
}

func tasksDispatch(st *store.Store, lookup func(int64) string, promptFn PromptFunc) Handler {
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
			page := 0
			if len(args) > 0 {
				var err error
				page, err = strconv.Atoi(args[0])
				if err != nil || page < 0 {
					return Response{Text: "Invalid task page. Use /tasks.", Code: 1}, nil
				}
			}
			return tasksListPage(ctx, st, folder, page)
		case "add":
			return tasksAdd(ctx, st, folder, cmd.Caller, args, promptFn)
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

const taskPageSize = 3

func tasksList(ctx context.Context, st *store.Store, folder string) (Response, error) {
	return tasksListPage(ctx, st, folder, 0)
}

func tasksListPage(ctx context.Context, st *store.Store, folder string, page int) (Response, error) {
	tasks, err := st.ListTasksByChat(ctx, folder)
	if err != nil {
		return Response{}, err
	}
	if len(tasks) == 0 {
		return Response{Text: "No scheduled tasks for this chat.\nCreate one: /tasks add <description and timezone>", Buttons: [][]Button{{{Label: "Refresh", Command: "tasks"}}}}, nil
	}
	pages := (len(tasks) + taskPageSize - 1) / taskPageSize
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Scheduled tasks — page %d/%d (%d total)\n\n", page+1, pages, len(tasks))
	var buttons [][]Button
	end := min((page+1)*taskPageSize, len(tasks))
	for i := page * taskPageSize; i < end; i++ {
		t := tasks[i]
		next := "—"
		if t.NextRun > 0 {
			next = time.UnixMilli(t.NextRun).UTC().Format(time.RFC3339)
		}
		fmt.Fprintf(&sb, "%d. [%s] %s\n%s %s\n%s\nNext (UTC): %s\n\n", i+1, t.Status, t.ID, truncate(t.ScheduleType, 12), truncate(t.ScheduleValue, 60), truncate(t.Prompt, 70), next)
		label, target, style := "", "", ""
		switch t.Status {
		case "active":
			label, target, style = "Pause", "paused", StylePrimary
		case "paused":
			label, target, style = "Resume", "active", StyleSuccess
		}
		if target != "" {
			buttons = append(buttons, []Button{{Label: fmt.Sprintf("%d · %s", i+1, label), Command: "tasks.state", Args: []string{t.ID, t.Status, target, strconv.Itoa(page)}, Style: style}})
		}
	}
	var nav []Button
	if page > 0 {
		nav = append(nav, Button{Label: "‹ Previous", Command: "tasks", Args: []string{"list", strconv.Itoa(page - 1)}})
	}
	nav = append(nav, Button{Label: fmt.Sprintf("↻ %d/%d", page+1, pages), Command: "tasks", Args: []string{"list", strconv.Itoa(page)}})
	if page+1 < pages {
		nav = append(nav, Button{Label: "Next ›", Command: "tasks", Args: []string{"list", strconv.Itoa(page + 1)}})
	}
	buttons = append(buttons, nav)
	return Response{Text: sb.String(), Buttons: buttons}, nil
}

func tasksStateButton(st *store.Store, lookup func(int64) string) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		folder := lookup(cmd.Caller.ChatID)
		if folder == "" {
			return Response{Text: "This chat is not registered.", Code: 1}, nil
		}
		if len(cmd.Args) != 4 {
			return Response{Text: "Task button expired. Use /tasks.", Code: 1}, nil
		}
		page, err := strconv.Atoi(cmd.Args[3])
		if err != nil || page < 0 {
			return Response{Text: "Invalid task page. Use /tasks.", Code: 1}, nil
		}
		expected, target := cmd.Args[1], cmd.Args[2]
		if !((expected == "active" && target == "paused") || (expected == "paused" && target == "active")) {
			return Response{Text: "Invalid task state action.", Code: 1}, nil
		}
		changed, err := tasksSetStatusExpected(ctx, st, folder, []string{cmd.Args[0]}, target, expected)
		if err != nil {
			return Response{}, err
		}
		panel, err := tasksListPage(ctx, st, folder, page)
		if err != nil {
			return Response{}, err
		}
		if changed.Code != 0 {
			panel.Text = changed.Text + "\n\n" + panel.Text
			panel.Code = changed.Code
		}
		return panel, nil
	}
}

// parsePrompt is the system prompt sent to Claude to parse natural
// language scheduling requests into structured JSON.
const parsePrompt = `You are a scheduling parser. The user wants to create a scheduled task.
Parse their request into this exact JSON format (no markdown, no explanation, ONLY the JSON object):
{"type":"cron","schedule":"<5-field cron expression, optionally prefixed CRON_TZ=IANA/Zone>","prompt":"<the task prompt>"}
OR {"type":"interval","schedule":"<Go duration like 1h or 30m>","prompt":"<the task prompt>"}
OR {"type":"once","schedule":"<RFC3339 datetime like 2026-12-25T09:00:00Z>","prompt":"<the task prompt>"}

Rules:
- "type" must be exactly one of: "cron", "interval", "once"
- For cron: use standard 5-field (minute hour dom month dow). Preserve an explicit user timezone as CRON_TZ=IANA/Zone, e.g. "CRON_TZ=Europe/Belgrade 0 9 * * *". Bare cron uses the current process timezone shown below, not a guessed Telegram/device timezone.
- For interval: use Go duration format, e.g. "1h", "30m", "2h30m"
- For once: use RFC3339 with an explicit offset. Preserve any user-specified timezone using the offset valid at that date (including DST). Assume UTC only if no timezone was specified.
- "prompt" is what the agent should be asked to do when the task fires
- Extract the prompt from the user's natural language, removing the scheduling part
- Current time is: %s

User request: %s`

// parsedSchedule is the JSON Claude returns from the parse prompt.
type parsedSchedule struct {
	Type     string `json:"type"`
	Schedule string `json:"schedule"`
	Prompt   string `json:"prompt"`
}

func tasksAdd(ctx context.Context, st *store.Store, folder string, caller Caller, args []string, promptFn PromptFunc) (Response, error) {
	if len(args) == 0 {
		return Response{
			Text: "Usage: /tasks add <description in natural language>\n\n" +
				"Examples:\n" +
				"  /tasks add check weather every morning at 9\n" +
				"  /tasks add remind me to stretch every hour\n" +
				"  /tasks add say happy new year on 2027-01-01 at midnight\n",
			Code: 1,
		}, nil
	}

	// Reject a full or missing non-owner registration before spending a parser turn.
	if err := scheduler.CheckTaskBudget(ctx, st, store.TaskRecord{ChatFolder: folder, ChatJID: fmt.Sprintf("tg:%d", caller.ChatID), ScheduleType: "once"}); err != nil {
		return Response{Text: err.Error(), Code: 1}, nil
	}
	userText := strings.Join(args, " ")
	now := time.Now()

	var parsed parsedSchedule

	// If promptFn is available, use Claude to parse natural language.
	if promptFn != nil {
		llmPrompt := fmt.Sprintf(parsePrompt, now.Format(time.RFC3339), userText)
		reply, err := promptFn(ctx, folder, llmPrompt)
		if err != nil {
			return Response{
				Text: fmt.Sprintf("Failed to parse schedule (agent error): %v\n\nTry a simpler phrasing.", err),
				Code: 1,
			}, nil
		}

		// Extract JSON from reply — Claude might wrap it in markdown.
		jsonStr := extractJSON(reply)
		if jsonStr == "" {
			return Response{
				Text: "Could not parse schedule from agent reply:\n" + truncate(reply, 200) +
					"\n\nTry a simpler phrasing.",
				Code: 1,
			}, nil
		}

		if err := json.Unmarshal([]byte(jsonStr), &parsed); err != nil {
			return Response{
				Text: "Agent returned invalid JSON: " + truncate(reply, 200),
				Code: 1,
			}, nil
		}
	} else {
		// Fallback: manual syntax (cron/interval/once + value + prompt).
		if len(args) < 3 {
			return Response{
				Text: "No agent available for natural language parsing.\n" +
					"Manual syntax: /tasks add <cron|interval|once> <value> <prompt>",
				Code: 1,
			}, nil
		}
		parsed = parsedSchedule{
			Type:     args[0],
			Schedule: args[1],
			Prompt:   strings.Join(args[2:], " "),
		}
	}

	now = time.Now()
	nextRun, err := scheduler.FirstRunAt(parsed.Type, parsed.Schedule, now)
	if err != nil {
		return Response{Text: fmt.Sprintf("Invalid schedule: %v", err), Code: 1}, nil
	}

	if parsed.Prompt == "" {
		return Response{Text: "Parser returned empty prompt. Try rephrasing.", Code: 1}, nil
	}

	chatJID := fmt.Sprintf("tg:%d", caller.ChatID)
	release := scheduler.LockTasks(st)
	defer release()
	rec := store.TaskRecord{
		ChatFolder:    folder,
		ChatJID:       chatJID,
		Prompt:        parsed.Prompt,
		ScheduleType:  parsed.Type,
		ScheduleValue: parsed.Schedule,
		NextRun:       nextRun,
		Status:        "active",
	}
	if err := scheduler.CheckTaskBudget(ctx, st, rec); err != nil {
		return Response{Text: err.Error(), Code: 1}, nil
	}
	id, err := st.CreateTask(ctx, rec)
	if err != nil {
		return Response{}, err
	}

	rec.ID = id
	scheduler.NotifyTaskCreated(ctx, st, rec)
	nextStr := time.UnixMilli(nextRun).UTC().Format(time.RFC3339)
	return Response{
		Text: fmt.Sprintf("Task created: %s\nType: %s %s\nNext run (UTC): %s\nPrompt: %s",
			id, parsed.Type, parsed.Schedule, nextStr, truncate(parsed.Prompt, 80)),
	}, nil
}

// extractJSON finds the first {...} block in s. Claude sometimes wraps
// JSON in markdown code fences; this strips them.
func extractJSON(s string) string {
	s = strings.TrimSpace(s)
	// Strip markdown ```json ... ``` wrapper if present.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s[3:], "\n"); i >= 0 {
			s = s[3+i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	// Find first { and last }.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func tasksSetStatus(ctx context.Context, st *store.Store, folder string, args []string, status string) (Response, error) {
	return tasksSetStatusExpected(ctx, st, folder, args, status, "")
}

func tasksSetStatusExpected(ctx context.Context, st *store.Store, folder string, args []string, status, expected string) (Response, error) {
	release := scheduler.LockTasks(st)
	defer release()
	if len(args) == 0 {
		return Response{Text: "Usage: /tasks " + status + " <task_id>", Code: 1}, nil
	}
	id := args[0]

	// Verify task belongs to this chat.
	t, err := st.GetTask(ctx, id)
	if err != nil || t.ChatFolder != folder {
		return Response{Text: "Task not found. Use full ID from /tasks list.", Code: 1}, nil
	}

	if t.Status != "active" && t.Status != "paused" {
		return Response{Text: "Completed tasks cannot be resumed or paused. Create a new task.", Code: 1}, nil
	}
	if expected != "" && t.Status != expected {
		return Response{Text: "Task state changed; list refreshed. No change made.", Code: 1}, nil
	}
	if t.Status == status {
		return Response{Text: fmt.Sprintf("Task %s is already %s", id, status)}, nil
	}

	nextRun := t.NextRun
	if status == "active" && t.Status == "paused" {
		if err := scheduler.CheckTaskBudget(ctx, st, t); err != nil {
			return Response{Text: err.Error(), Code: 1}, nil
		}
		// Resuming: recompute next_run from now.
		now := time.Now().UnixMilli()
		nextRun, _ = computeNextRunForResume(t, now)
		if nextRun == 0 {
			return Response{Text: "Task has no future run. Update its schedule or create a new task.", Code: 1}, nil
		}
	}
	if status == "paused" {
		nextRun = 0 // paused tasks don't fire
	}

	if err := st.UpdateTaskStatus(ctx, id, status, nextRun, t.LastRun); err != nil {
		return Response{}, err
	}
	return Response{Text: fmt.Sprintf("Task %s → %s", id, status)}, nil
}

func tasksCancel(ctx context.Context, st *store.Store, folder string, args []string) (Response, error) {
	release := scheduler.LockTasks(st)
	defer release()
	if len(args) == 0 {
		return Response{Text: "Usage: /tasks cancel <task_id>", Code: 1}, nil
	}
	id := args[0]

	t, err := st.GetTask(ctx, id)
	if err != nil || t.ChatFolder != folder {
		return Response{Text: "Task not found.", Code: 1}, nil
	}

	if err := st.DeleteTask(ctx, id); err != nil {
		if errors.Is(err, store.ErrManagedTask) {
			return Response{Text: "Managed tasks cannot be deleted by this command.", Code: 1}, nil
		}
		return Response{}, err
	}
	return Response{Text: fmt.Sprintf("Task %s cancelled and deleted.", id)}, nil
}

// computeNextRunForResume recomputes next_run when a paused task is
// resumed. For cron: next occurrence from now. For interval: now + interval.
// For once: if the original time is in the past, keep it (will fire immediately).
func computeNextRunForResume(t store.TaskRecord, now int64) (int64, string) {
	next, err := scheduler.FirstRunAt(t.ScheduleType, t.ScheduleValue, time.UnixMilli(now))
	if err != nil {
		return 0, "done"
	}
	return next, "active"

}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
