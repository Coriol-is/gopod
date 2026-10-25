package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

func (a *IPCSink) ProcessTaskRequest(ctx context.Context, id ipc.TaskRequestIdentity, apply func(context.Context) (ipc.TaskResult, error)) (ipc.TaskResult, error) {
	release := taskOperationLock(ctx, a.st)
	defer release()
	ctx = context.WithValue(ctx, taskLockKey{}, a.st)
	data, err := a.st.ApplyTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention, func(inner context.Context) ([]byte, error) {
		result, err := apply(inner)
		if err != nil {
			return nil, err
		}
		return json.Marshal(result)
	})
	if errors.Is(err, store.ErrTaskRequestConflict) {
		return ipc.TaskResult{}, fmt.Errorf("use a new unique request filename for each new operation: %w", ipc.ErrPermanent)
	}
	if err != nil {
		return ipc.TaskResult{}, err
	}
	var result ipc.TaskResult
	if err = json.Unmarshal(data, &result); err != nil {
		return result, fmt.Errorf("decoding stored IPC result: %w", err)
	}
	return result, nil
}

func (a *IPCSink) AcknowledgeTaskRequest(ctx context.Context, id ipc.TaskRequestIdentity) error {
	release := taskOperationLock(ctx, a.st)
	defer release()
	return a.st.AcknowledgeTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention)
}
