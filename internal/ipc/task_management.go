package ipc

import (
	"context"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type TaskScope struct {
	SourceFolder string
	IsOwner      bool
}
type taskScopeKey struct{}
type taskTargetJIDKey struct{}

func TaskScopeFromContext(ctx context.Context) (TaskScope, bool) {
	s, ok := ctx.Value(taskScopeKey{}).(TaskScope)
	return s, ok
}

// TaskInfo is the agent-visible task definition. Access is scoped before this
// object is returned; lists never expose another chat's prompts to non-owners.
type TaskInfo struct {
	TaskID        string `json:"taskId"`
	ChatFolder    string `json:"chatFolder"`
	ChatJID       string `json:"chatJid"`
	Prompt        string `json:"prompt"`
	ScheduleType  string `json:"scheduleType"`
	ScheduleValue string `json:"scheduleValue"`
	NextRun       int64  `json:"nextRun"`
	NextRunAt     string `json:"nextRunAt,omitempty"`
	Status        string `json:"status"`
}

func DescribeTask(t store.TaskRecord) TaskInfo {
	info := TaskInfo{TaskID: t.ID, ChatFolder: t.ChatFolder, ChatJID: t.ChatJID, Prompt: t.Prompt, ScheduleType: t.ScheduleType, ScheduleValue: t.ScheduleValue, NextRun: t.NextRun, Status: t.Status}
	if t.NextRun > 0 {
		info.NextRunAt = time.UnixMilli(t.NextRun).UTC().Format(time.RFC3339)
	}
	return info
}

type TaskResult struct {
	TaskID  string     `json:"taskId,omitempty"`
	Task    *TaskInfo  `json:"task,omitempty"`
	Tasks   []TaskInfo `json:"tasks,omitempty"`
	Deleted bool       `json:"deleted,omitempty"`
}

// TaskManager is additive to the original Schedule/Cancel sink. The source
// scope is set from the IPC directory, never from JSON fields.
type TaskManager interface {
	Manage(context.Context, string, store.TaskRecord) (TaskResult, error)
}

// TaskRequestIdentity is constructed from the source directory and submitted
// bytes; these fields never come from agent JSON.
type TaskRequestIdentity struct {
	SourceFolder, File, PayloadHash string
	Retention                       int
}
type TaskRequestProcessor interface {
	ProcessTaskRequest(context.Context, TaskRequestIdentity, func(context.Context) (TaskResult, error)) (TaskResult, error)
	AcknowledgeTaskRequest(context.Context, TaskRequestIdentity) error
}
