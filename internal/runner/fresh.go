package runner

import (
	"context"
	"fmt"
	"time"
)

// Fresh system work must never hold the chat indefinitely. Caller deadlines
// can shorten this bound; foreground work also cancels the same-chat run.
const freshRunTimeout = time.Minute

func (r *Runner) execAgentWithCancel(ctx context.Context, id, folder string, thread int, prov AgentProvider, cmd []string, interactive bool) (*ExecResult, error) {
	pidFile, err := newAgentPidFile()
	if err != nil {
		return nil, err
	}
	finished := make(chan struct{})
	cleanup := make(chan bool, 1)
	go func() {
		select {
		case <-ctx.Done():
		case <-finished:
		}
		if ctx.Err() != nil {
			r.killAgent(ctx, id, folder, pidFile)
			cleanup <- true
		} else {
			cleanup <- false
		}
	}()
	res, err := r.execAgent(ctx, id, folder, thread, prov, wrapWithPidFile(pidFile, cmd), interactive)
	close(finished)
	killed := <-cleanup
	if !killed {
		r.removePidFile(ctx, id, pidFile)
	}
	if ctx.Err() != nil {
		return nil, fmt.Errorf("runner: agent cancelled: %w", ctx.Err())
	}
	return res, err
}
