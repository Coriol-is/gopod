package telegram

import (
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
// is cancelled. It never waits for native exit. Late NoReplay is logged for
// reconciliation; retroactive queue status changes require the future lifecycle
// settlement integration before foreground freeze can be enabled.
func (b *Bot) discardCancelledStream(item queue.Item, stream *runner.AgentStream) func(error) error {
	completed := make(chan runner.RunResult, 1)
	select {
	case result := <-stream.Result:
		completed <- result
		go drainStream(stream)
	default:
		go func() {
			drainStream(stream)
			result := <-stream.Result
			if noReplayError(result.Err) {
				b.log.Warn("cancelled stream completed with non-replayable result failure; reconciliation required", slog.Int64("turn", item.ID))
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
