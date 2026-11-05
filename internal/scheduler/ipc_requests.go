package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

type taskNotificationsKey struct{}

func (a *IPCSink) ProcessTaskRequest(ctx context.Context, id ipc.TaskRequestIdentity, apply func(context.Context) (ipc.TaskResult, error)) (ipc.TaskResult, error) {
	release := taskOperationLock(ctx, a.st)
	defer release()
	ctx = context.WithValue(ctx, taskLockKey{}, a.st)
	var notifications []store.TaskRecord
	ctx = context.WithValue(ctx, taskNotificationsKey{}, &notifications)
	applyBytes := func(inner context.Context) ([]byte, error) {
		result, err := apply(inner)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	}
	var data []byte
	var err error
	if id.Scoped {
		data, err = a.st.ApplyScopedTaskRequest(ctx, id.Scope, id.SourceFolder, id.File, id.PayloadHash, id.Retention, applyBytes)
	} else {
		data, err = a.st.ApplyTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention, applyBytes)
	}
	var conflict *store.TaskRequestConflict
	if errors.As(err, &conflict) {
		id.PayloadHash = conflict.OriginalHash
		return ipc.TaskResult{}, &ipc.TaskRequestConflict{Identity: id}
	}
	if err != nil {
		return ipc.TaskResult{}, err
	}
	var result ipc.TaskResult
	if err = json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decoding stored IPC result: %w", err)
	}
	for _, t := range notifications {
		NotifyTaskCreated(ctx, a.st, t)
	}
	return result, nil
}

func (a *IPCSink) AcknowledgeTaskRequest(ctx context.Context, id ipc.TaskRequestIdentity) error {
	release := taskOperationLock(ctx, a.st)
	defer release()
	if id.Scoped {
		return a.st.AcknowledgeScopedTaskRequest(ctx, id.Scope, id.SourceFolder, id.File, id.PayloadHash, id.Retention)
	}
	return a.st.AcknowledgeTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention)
}
