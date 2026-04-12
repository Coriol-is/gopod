package control

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/spaceinvaderz/picoclaw/internal/scheduler"
	"github.com/spaceinvaderz/picoclaw/internal/store"
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
// it into a structured {type, schedule, prompt} JSON. picoclaw then
// validates the parsed values before persisting. If promptFn is nil,
// falls back to the old manual syntax (cron/interval/once + value).
func RegisterTaskCommands(r *Router, st *store.Store, chatFolderLookup func(chatID int64) string, promptFn PromptFunc) {
	r.Register("tasks", "tasks", "manage scheduled tasks (try: /tasks add)", PermChatLocal,
		tasksDispatch(st, chatFolderLookup, promptFn))
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
			return tasksList(ctx, st, folder)
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

// parsePrompt is the system prompt sent to Claude to parse natural
// language scheduling requests into structured JSON.
const parsePrompt = `You are a scheduling parser. The user wants to create a scheduled task.
Parse their request into this exact JSON format (no markdown, no explanation, ONLY the JSON object):
{"type":"cron","schedule":"<5-field cron expression>","prompt":"<the task prompt>"}
OR {"type":"interval","schedule":"<Go duration like 1h or 30m>","prompt":"<the task prompt>"}
OR {"type":"once","schedule":"<RFC3339 datetime like 2026-12-25T09:00:00Z>","prompt":"<the task prompt>"}

Rules:
- "type" must be exactly one of: "cron", "interval", "once"
- For cron: use standard 5-field (minute hour dom month dow), e.g. "0 9 * * *" for daily at 9am
- For interval: use Go duration format, e.g. "1h", "30m", "2h30m"
- For once: use RFC3339, assume UTC if no timezone given
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

	// Validate the parsed result.
	var nextRun int64

	switch parsed.Type {
	case "cron":
		if err := scheduler.ValidateCron(parsed.Schedule); err != nil {
			return Response{Text: fmt.Sprintf("Invalid cron from parser: %v\nTry rephrasing.", err), Code: 1}, nil
		}
		sched, _ := cronParserForAdd().Parse(parsed.Schedule)
		nextRun = sched.Next(now).UnixMilli()

	case "interval":
		if err := scheduler.ValidateInterval(parsed.Schedule); err != nil {
			return Response{Text: fmt.Sprintf("Invalid interval from parser: %v\nTry rephrasing.", err), Code: 1}, nil
		}
		d, _ := time.ParseDuration(parsed.Schedule)
		nextRun = now.Add(d).UnixMilli()

	case "once":
		t, err := time.Parse(time.RFC3339, parsed.Schedule)
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04", parsed.Schedule)
			if err != nil {
				return Response{
					Text: fmt.Sprintf("Invalid datetime from parser: %q\nTry rephrasing.", parsed.Schedule),
					Code: 1,
				}, nil
			}
		}
		nextRun = t.UnixMilli()

	default:
		return Response{
			Text: fmt.Sprintf("Parser returned unknown type %q. Try rephrasing.", parsed.Type),
			Code: 1,
		}, nil
	}

	if parsed.Prompt == "" {
		return Response{Text: "Parser returned empty prompt. Try rephrasing.", Code: 1}, nil
	}

	chatJID := fmt.Sprintf("tg:%d", caller.ChatID)
	id, err := st.CreateTask(ctx, store.TaskRecord{
		ChatFolder:    folder,
		ChatJID:       chatJID,
		Prompt:        parsed.Prompt,
		ScheduleType:  parsed.Type,
		ScheduleValue: parsed.Schedule,
		NextRun:       nextRun,
		Status:        "active",
	})
	if err != nil {
		return Response{}, err
	}

	nextStr := time.UnixMilli(nextRun).Format("2006-01-02 15:04")
	return Response{
		Text: fmt.Sprintf("Task created: %s\nType: %s %s\nNext run: %s\nPrompt: %s",
			id[:8], parsed.Type, parsed.Schedule, nextStr, truncate(parsed.Prompt, 80)),
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
