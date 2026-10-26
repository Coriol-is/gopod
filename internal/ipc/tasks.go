package ipc

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

type taskPayload struct {
	Op               string `json:"op"`
	TaskID           string `json:"taskId"`
	Prompt           string `json:"prompt"`
	ScheduleType     string `json:"scheduleType"`
	ScheduleValue    string `json:"scheduleValue"`
	TargetChatFolder string `json:"targetChatFolder"`
}

func (w *Watcher) processTaskFile(ctx context.Context, folder, path string) (completed bool) {
	raw, err := w.readTask(path, folder)
	if err != nil {
		cause := fmt.Errorf("cannot read task request: publish a regular non-symlink JSON file atomically inside its IPC namespace")
		w.writeTaskReceipt(folder, path, "", nil, cause)
		w.moveFailed(folder, path, fmt.Errorf("task request failed; inspect source-scoped result receipt"))
		return true
	}

	var p taskPayload
	fail := func(cause error) bool {
		w.writeTaskReceipt(folder, path, p.Op, nil, cause)
		w.moveFailed(folder, path, fmt.Errorf("task request failed; inspect source-scoped result receipt"))
		return true
	}
	if len(raw) > 64<<10 {
		return fail(fmt.Errorf("task request exceeds 64 KiB"))
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return fail(fmt.Errorf("invalid task JSON: %w; expected op, taskId, prompt, scheduleType, scheduleValue, targetChatFolder", err))
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return fail(fmt.Errorf("one JSON task object is required"))
	}
	sourceJID, sourceOK := w.resolve(folder)
	if !sourceOK {
		return fail(&errUnauthorized{reason: "source folder not registered"})
	}
	// Group IDs are host-assigned from this namespace and request filename.
	// A user-selected global ID would reveal whether a private task already uses
	// that ID by collision/success responses. Returned IDs remain valid for get,
	// pause/resume/update/cancel/delete; only schedule creation omits taskId.
	if p.Op == "schedule" && p.TaskID != "" && folder != w.cfg.OwnerFolder && strings.HasPrefix(sourceJID, "tg:-") {
		return fail(&errUnauthorized{reason: "group schedules must omit taskId; use the host-returned task ID for management"})
	}
	targetJID := ""
	if p.Op == "schedule" || p.Op == "list" {
		if err := authzTaskSchedule(folder, p.TargetChatFolder, w.cfg.OwnerFolder, w.resolve); err != nil {
			return fail(err)
		}
		target := p.TargetChatFolder
		if target == "" {
			target = folder
		}
		targetJID, _ = w.resolve(target)
	}
	ctx = context.WithValue(ctx, taskTargetJIDKey{}, targetJID)
	ctx = context.WithValue(ctx, taskScopeKey{}, TaskScope{SourceFolder: folder, IsOwner: folder == w.cfg.OwnerFolder})
	if p.Op == "schedule" && p.TaskID == "" {
		// Stable per submitted filename: a receipt/archive retry cannot create a
		// second schedule after the first database commit already succeeded.
		sum := sha256.Sum256([]byte(folder + "/" + filepath.Base(path)))
		p.TaskID = fmt.Sprintf("task-%x", sum[:12])
	}
	apply := func(ctx context.Context) (TaskResult, error) {
		var dispatchErr error
		var result TaskResult
		switch p.Op {
		case "schedule":
			dispatchErr = w.handleTaskSchedule(ctx, folder, p)
			result.TaskID = p.TaskID
			if dispatchErr == nil {
				if manager, ok := w.tasks.(TaskManager); ok {
					result, dispatchErr = manager.Manage(ctx, "get", store.TaskRecord{ID: p.TaskID})
				}
			}
		case "cancel":
			dispatchErr = w.handleTaskCancel(ctx, folder, p)
			result.TaskID = p.TaskID
			if dispatchErr == nil {
				if manager, ok := w.tasks.(TaskManager); ok {
					result, dispatchErr = manager.Manage(ctx, "get", store.TaskRecord{ID: p.TaskID})
				}
			}
		case "list", "get", "pause", "resume", "update", "delete":

			if dispatchErr == nil {
				if manager, ok := w.tasks.(TaskManager); ok {
					result, dispatchErr = manager.Manage(ctx, p.Op, store.TaskRecord{ID: p.TaskID, ChatFolder: p.TargetChatFolder, Prompt: p.Prompt, ScheduleType: p.ScheduleType, ScheduleValue: p.ScheduleValue})
				} else {
					dispatchErr = fmt.Errorf("task management unavailable: %w", ErrPermanent)
				}
			}
		default:
			return TaskResult{}, fmt.Errorf("unknown op %q: use schedule, list, get, update, pause, resume, cancel or delete: %w", p.Op, ErrPermanent)
		}

		return result, dispatchErr

	}
	var result TaskResult
	var dispatchErr error
	processor, hasProcessor := w.tasks.(TaskRequestProcessor)
	var identity TaskRequestIdentity
	if hasProcessor {
		hash := sha256.Sum256(append([]byte(fmt.Sprintf("%s/%s/owner=%t\n", sourceJID, targetJID, folder == w.cfg.OwnerFolder)), raw...))
		identity = TaskRequestIdentity{SourceFolder: folder, File: filepath.Base(path), PayloadHash: fmt.Sprintf("%x", hash[:]), Retention: w.cfg.Retention}
		result, dispatchErr = processor.ProcessTaskRequest(ctx, identity, apply)
	} else {
		result, dispatchErr = apply(ctx)
	}

	if dispatchErr == nil {
		if !w.writeTaskReceipt(folder, path, p.Op, &result, nil) {
			return false
		}
		if w.moveProcessed(folder, path) && hasProcessor {
			if err := processor.AcknowledgeTaskRequest(ctx, identity); err != nil {
				w.log.Warn("ipc: acknowledge task receipt", "folder", folder, "file", filepath.Base(path), "err", err)
			}
		}
		return true
	}
	if errors.Is(dispatchErr, ErrPermanent) {
		return fail(dispatchErr)
	}
	if w.bumpRetry(path) {
		return fail(fmt.Errorf("retries exhausted: %w", dispatchErr))
	}
	w.log.Warn("ipc: task dispatch transient error",
		"folder", folder, "channel", "tasks", "file", filepath.Base(path),
		"attempts", w.retries[path].attempts)
	return false
}

func (w *Watcher) handleTaskSchedule(ctx context.Context, folder string, p taskPayload) error {
	target := p.TargetChatFolder
	if target == "" {
		target = folder
	}
	jid, _ := ctx.Value(taskTargetJIDKey{}).(string)
	if jid == "" {
		return fmt.Errorf("missing validated task target: %w", ErrPermanent)
	}

	id := p.TaskID
	if id == "" {
		id = newTaskID(w.cfg.NowFn().UnixMilli())
	}
	rec := store.TaskRecord{
		ID:            id,
		ChatFolder:    target,
		ChatJID:       jid,
		Prompt:        p.Prompt,
		ScheduleType:  p.ScheduleType,
		ScheduleValue: p.ScheduleValue,
		Status:        "active",
		CreatedAt:     w.cfg.NowFn().UnixMilli(),
	}
	return w.tasks.Schedule(ctx, rec)
}

func (w *Watcher) handleTaskCancel(ctx context.Context, folder string, p taskPayload) error {
	if p.TaskID == "" {
		return fmt.Errorf("cancel: empty taskId: %w", ErrPermanent)
	}
	// The sink receives source scope in the host-created context and checks
	// the stored task folder before cancellation. JSON cannot set this scope.
	_ = folder

	return w.tasks.Cancel(ctx, p.TaskID)
}

func newTaskID(nowMs int64) string {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("task-%d-%s", nowMs, hex.EncodeToString(b[:]))
}
