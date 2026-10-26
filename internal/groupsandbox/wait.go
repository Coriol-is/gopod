package groupsandbox

import (
	"context"
	"github.com/docker/docker/api/types/container"
	"time"
)

func (e *Executor) waitExec(ctx context.Context, id string) (container.ExecInspect, error) {
	for {
		state, err := e.Client.ContainerExecInspect(ctx, id)
		if err != nil || !state.Running {
			return state, err
		}
		select {
		case <-ctx.Done():
			return state, ErrUnavailable
		case <-time.After(10 * time.Millisecond):
		}
	}
}
