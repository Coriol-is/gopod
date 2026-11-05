package telegram

import (
	"context"
	"errors"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// noReplayError survives transport error presentation and shutdown cancellation:
// the runner has already completed and must not be invoked again.
func noReplayError(err error) bool {
	var marker interface{ NoReplay() bool }
	return errors.As(err, &marker) && marker.NoReplay()
}

// discardCancelledStream preserves a completion already available when a stream
// is cancelled. It never waits for native exit. Late completion is persisted against any accepted Stop intent without claiming
// native exit. NoReplay remains evidence even after the handler returned.
func (b *Bot) discardCancelledStream(ctx context.Context, item queue.Item, stream *runner.AgentStream) func(error) error {
	completed := make(chan runner.RunResult, 1)
	select {
	case result := <-stream.Result:
		if err := b.observeStopCompletion(ctx, result); err != nil {
			result.Err = errors.Join(result.Err, err)
		}
		completed <- result
		go drainStream(stream)
	default:
		go func() {
			drainStream(stream)
			result := <-stream.Result
			if noReplayError(result.Err) {
				b.log.Warn("cancelled stream completed with non-replayable result failure; reconciliation required", slog.Int64("turn", item.ID))
			}
			if err := b.observeStopCompletion(ctx, result); err != nil {
				result.Err = errors.Join(result.Err, err)
			}
			completed <- result
		}()
	}
	return func(fallback error) error {
		select {
		case result := <-completed:
			if noReplayError(result.Err) {
				return result.Err
			}
		default:
		}
		return fallback
	}
}

// An unconfirmed Stop must not return into runAgentStreaming's deferred native
// cancel. Remain attached until the native completion arrives; shutdown still
// propagates through the original native context independently of Stop.
func (b *Bot) drainHeldStream(ctx context.Context, item queue.Item, stream *runner.AgentStream, hold error) error {
	completed := make(chan error, 1)
	go func() {
		drainStream(stream)
		result := <-stream.Result
		if noReplayError(result.Err) {
			b.log.Warn("held stream returned NoReplay evidence", "turn", item.ID)
		}
		completed <- errors.Join(hold, result.Err, b.observeStopCompletion(ctx, result))
	}()
	select {
	case err := <-completed:
		return err
	case <-ctx.Done():
		return errors.Join(hold, ctx.Err())
	}
}
