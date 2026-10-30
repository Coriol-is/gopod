package groupruntime

import (
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/store"
)

var receiptTaskID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

type receiptTask struct {
	TaskID       string `json:"taskId,omitempty"`
	Prompt       string `json:"prompt,omitempty"`
	NextRunAt    string `json:"nextRunAt,omitempty"`
	ChatFolder   string `json:"chatFolder,omitempty"`
	ChatJID      string `json:"chatJid,omitempty"`
	Status       string `json:"status,omitempty"`
	ScheduleType string `json:"scheduleType,omitempty"`
	NextRun      int64  `json:"nextRun,omitempty"`
}
type receiptResult struct {
	TaskID  string        `json:"taskId,omitempty"`
	Task    *receiptTask  `json:"task,omitempty"`
	Tasks   []receiptTask `json:"tasks,omitempty"`
	Deleted bool          `json:"deleted,omitempty"`
}

// Only typed bounded management metadata crosses this boundary. Legacy errors,
// help text and unknown future fields are never returned verbatim.
// Group-owned prompts pass the same bounded secret guard as publication.
func projectReceipt(raw []byte, channel, file string, scope store.ScopeRef) (string, error) {
	var input struct {
		Status string          `json:"status"`
		Op     string          `json:"op"`
		Result *receiptResult  `json:"result"`
		Error  json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &input) != nil {
		return "", ErrUnavailable
	}
	switch input.Status {
	case "ok", "error", "retrying", "pending", "uncertain":
	default:
		return "", ErrUnavailable
	}
	out := map[string]any{"status": input.Status, "requestFile": strings.TrimSuffix(file, ".result.json") + ".json", "help": "Queued is not completed or delivered. Missing, failed or uncertain receipts do not authorize blind replay."}
	hasError := len(input.Error) > 0 && string(input.Error) != "null" && string(input.Error) != `""`
	if input.Status != "ok" || hasError {
		out["error"] = "operation_unavailable"
	}
	if channel == "messages" {
		out["delivery"] = "unconfirmed"
		if input.Status == "ok" && !hasError {
			out["delivery"] = "acknowledged"
		}
		b, _ := json.Marshal(out)
		return string(b), nil
	}
	switch input.Op {
	case "schedule", "list", "get", "update", "pause", "resume", "cancel", "delete":
		out["op"] = input.Op
	}
	if input.Result != nil {
		if sanitizeReceiptResult(input.Result, scope) != nil {
			return "", ErrUnavailable
		}
		out["result"] = input.Result
	}
	b, err := json.Marshal(out)
	if err != nil {
		return "", ErrUnavailable
	}
	return string(b), nil
}
func sanitizeReceiptResult(result *receiptResult, scope store.ScopeRef) error {
	if result.TaskID != "" && !receiptTaskID.MatchString(result.TaskID) || len(result.Tasks) > 128 {
		return ErrUnavailable
	}
	if result.Task != nil && sanitizeReceiptTask(result.Task, scope) != nil {
		return ErrUnavailable
	}
	for i := range result.Tasks {
		if sanitizeReceiptTask(&result.Tasks[i], scope) != nil {
			return ErrUnavailable
		}
	}
	return nil
}
func sanitizeReceiptTask(task *receiptTask, scope store.ScopeRef) error {
	if !receiptTaskID.MatchString(task.TaskID) || task.ChatFolder != "" && task.ChatFolder != scope.Folder || task.ChatJID != "" && task.ChatJID != scope.JID {
		return ErrUnavailable
	}
	if len(task.Prompt) > 4096 || memory.IsGroupSensitiveText(task.Prompt) || sensitive.MatchString(task.Prompt) {
		task.Prompt = "[content unavailable]"
	}
	task.NextRunAt = ""
	task.ChatFolder = ""
	task.ChatJID = ""
	switch task.Status {
	case "", "active", "paused", "done":
	default:
		return ErrUnavailable
	}
	switch task.ScheduleType {
	case "", "cron", "interval", "once":
	default:
		return ErrUnavailable
	}
	if task.NextRun < 0 { // internal pause marker: unscheduled, not a reason to drop the receipt
		task.NextRun = 0
	}
	if task.NextRun > 0 {
		task.NextRunAt = time.UnixMilli(task.NextRun).UTC().Format(time.RFC3339)
	}
	return nil
}
