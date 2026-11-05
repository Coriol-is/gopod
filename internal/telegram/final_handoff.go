package telegram

import (
	"context"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

type finalGenerationKey struct{}
type finalGeneration struct {
	turnID int64
	turn   *inflightTurn
}

// registerFinalGeneration binds the pointer returned by registration itself.
// A replacement before this function returns cannot change that identity. A new
// registration always replaces an inherited binding, even for the same turn ID.
func (b *Bot) registerFinalGeneration(ctx context.Context, item queue.Item, cancel context.CancelFunc) (context.Context, func()) {
	turn, unregister := b.registerInflightTurn(item.ID, item.ChatID, item.ThreadID, cancel, func(t *inflightTurn) { b.configureDurableStop(ctx, t) })
	return context.WithValue(ctx, finalGenerationKey{}, finalGeneration{item.ID, turn}), unregister
}

// finalHandoff binds the exact registration through sync/stream/fallback calls.
// Stop and final ownership are read from one snapshot, never separate lookups.
func (b *Bot) finalHandoff(ctx context.Context, turnID int64) (context.Context, func(context.Context) error, <-chan struct{}) {
	binding, ok := ctx.Value(finalGenerationKey{}).(finalGeneration)
	if !ok || binding.turnID != turnID {
		b.inflightMu.Lock()
		binding = finalGeneration{turnID, b.inflight[turnID]}
		b.inflightMu.Unlock()
		ctx = context.WithValue(ctx, finalGenerationKey{}, binding)
	}
	t := binding.turn
	var stop <-chan struct{}
	if t != nil {
		stop = t.stopped
	}
	return ctx, func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if t == nil {
			return nil
		}
		return t.beginFinal()
	}, stop
}
func finalGenerationRetired(ctx context.Context) bool {
	binding, ok := ctx.Value(finalGenerationKey{}).(finalGeneration)
	if !ok || binding.turn == nil {
		return false
	}
	binding.turn.phaseMu.Lock()
	defer binding.turn.phaseMu.Unlock()
	return binding.turn.retired
}

func (b *Bot) finishBufferedStopped(ctx context.Context, item queue.Item) error {
	stopped, cancel := context.WithCancel(ctx)
	cancel()
	_, err := b.streamNotStarted(ctx, stopped, item)
	return err
}

type finalOwnershipLost struct{}

func (finalOwnershipLost) Error() string  { return "telegram: final reply generation retired" }
func (finalOwnershipLost) NoReplay() bool { return true }

func (t *inflightTurn) beginFinal() error {
	for {
		t.phaseMu.Lock()
		pending := t.stopPending
		if pending == nil {
			break
		}
		t.phaseMu.Unlock()
		<-pending
	}
	defer t.phaseMu.Unlock()
	if t.stopErr != nil {
		return t.stopErr
	}
	if t.retired {
		return finalOwnershipLost{}
	}
	select {
	case <-t.stopped:
		return queue.ErrStopped
	default:
	}
	if t.finalizing {
		return finalOwnershipLost{}
	} // one final owner only
	t.finalizing = true
	return nil
}
func (t *inflightTurn) retire() { t.phaseMu.Lock(); t.retired = true; t.phaseMu.Unlock() }

// A result already consumed by streamReply must not be read a second time by
// stopped settlement (some runner/test streams do not close Result).
func settledResultStream(result runner.RunResult) *runner.AgentStream {
	chunks := make(chan string)
	close(chunks)
	results := make(chan runner.RunResult, 1)
	results <- result
	close(results)
	return &runner.AgentStream{Chunks: chunks, Result: results}
}
