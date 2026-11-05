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
	c, err := w.openTaskClaim(ctx, folder, filepath.Base(path), true)
	if err != nil {
		w.log.Warn("ipc: task claim held", "folder", folder, "err", err)
		return false
	}
	defer c.close()
	if c.record.Receipt != nil {
		return w.finishTaskClaim(ctx, c)
	}
	if !c.retryDue(w.cfg.NowFn()) {
		return false
	}
	if err := c.snapshot(); err != nil {
		w.log.Warn("ipc: task claim snapshot held", "folder", folder, "err", err)
		return false
	}
	raw := c.record.Snapshot
	var p taskPayload
	fail := func(cause error) bool { return w.completeTaskClaim(ctx, c, p.Op, nil, cause, nil) }
	if c.record.ReadError != "" {
		return fail(errors.New(c.record.ReadError))
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
	if p.Op == "schedule" && p.TaskID != "" && folder != w.cfg.OwnerFolder {
		return fail(&errUnauthorized{reason: "non-owner schedules must omit taskId; use the host-returned task ID for management"})
	}
	// preset-<id> task ids belong to the built-in proactive presets (CP-033), which
	// adopt a task found under their id. Only the owner folder may choose one.
	if p.Op == "schedule" && strings.HasPrefix(p.TaskID, "preset-") &&
		(folder != w.cfg.OwnerFolder || (p.TargetChatFolder != "" && p.TargetChatFolder != w.cfg.OwnerFolder)) {
		return fail(&errUnauthorized{reason: "task ids starting with preset- are reserved for the owner folder"})
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
	if err := w.validateTaskClaim(ctx, c); err != nil {
		return false
	}
	var dispatchErr error
	processor, hasProcessor := w.tasks.(TaskRequestProcessor)
	var identity TaskRequestIdentity
	if hasProcessor {
		hash := sha256.Sum256(append([]byte(fmt.Sprintf("%s/%s/owner=%t\n", sourceJID, targetJID, folder == w.cfg.OwnerFolder)), raw...))
		identity = TaskRequestIdentity{Scope: c.record.Scope, Scoped: c.record.Scoped, SourceFolder: folder, File: filepath.Base(path), PayloadHash: fmt.Sprintf("%x", hash[:]), Retention: w.cfg.Retention}
		result, dispatchErr = processor.ProcessTaskRequest(ctx, identity, apply)
	} else {
		result, dispatchErr = apply(ctx)
	}

	if err := c.checkpoint("dispatched"); err != nil {
		return false
	}
	if dispatchErr == nil {
		var ack *TaskRequestIdentity
		if hasProcessor {
			ack = &identity
		}
		return w.completeTaskClaim(ctx, c, p.Op, &result, nil, ack)
	}
	if errors.Is(dispatchErr, ErrPermanent) {
		var conflict *TaskRequestConflict
		if errors.As(dispatchErr, &conflict) {
			return w.completeTaskClaim(ctx, c, p.Op, nil, dispatchErr, &conflict.Identity)
		}
		return fail(dispatchErr)
	}
	if w.deferTaskClaim(c) {
		return fail(fmt.Errorf("retries exhausted: %w", dispatchErr))
	}
	w.log.Warn("ipc: task dispatch transient error", "folder", folder, "file", filepath.Base(path), "attempts", c.record.Attempts)
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
