package telegram

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

type ordinaryBatchKey struct{}
type stopPersistenceError struct{ err error }

func (e *stopPersistenceError) Error() string {
	return fmt.Sprintf("ordinary Stop requires reconciliation: %v", e.err)
}
func (e *stopPersistenceError) Unwrap() error  { return e.err }
func (e *stopPersistenceError) NoReplay() bool { return true }

func (b *Bot) bindOrdinaryBatch(ctx context.Context, items []queue.Item) (context.Context, error) {
	if b.store == nil || (len(items) == 1 && items[0].ID == 0) {
		return ctx, nil
	}
	first := items[0]
	conv, err := b.store.SnapshotConversation(ctx, buildChatJID(first.ChatID), first.ThreadID, store.VisibilityConversation)
	if err != nil {
		return ctx, err
	}
	binding := store.OrdinaryStopBinding{Conversation: conv}
	for _, it := range items {
		if it.ID <= 0 || it.Attempts <= 0 || it.AttemptID != "" || it.Source == "task" || it.ChatID != first.ChatID || it.Folder != conv.Scope.Folder || it.ThreadID != first.ThreadID {
			return ctx, store.ErrStopConflict
		}
		binding.Members = append(binding.Members, store.ResultMember{TurnID: it.ID, Attempt: it.Attempts})
	}
	ctx = runner.WithSelectionAttempts(ctx, binding.Members)
	return context.WithValue(ctx, ordinaryBatchKey{}, binding), nil
}

func (b *Bot) configureDurableStop(ctx context.Context, t *inflightTurn) {
	if t == nil {
		return
	}
	binding, ok := ctx.Value(ordinaryBatchKey{}).(store.OrdinaryStopBinding)
	if !ok || b.store == nil {
		return
	}
	t.persistStop = func(ctx context.Context) (string, error) { return recordStopIntent(ctx, b.store, binding) }
	t.persistCompletion = func(ctx context.Context, id string, result runner.RunResult) error {
		if id == "" {
			found, yes, err := b.store.FindOrdinaryStop(ctx, binding)
			if err != nil {
				return err
			}
			if !yes {
				return nil
			}
			id = found
		}
		outcome := "returned"
		if result.Err != nil {
			outcome = "error"
		}
		if noReplayError(result.Err) {
			outcome = "no_replay"
		}
		err := b.store.RecordStopCompletion(ctx, id, result.FullText, outcome)
		if err != nil {
			b.log.Error("record stopped completion failed", "err", err)
		}
		return err
	}
}

// requestStop never holds the map/queue locks during persistence. Pending
// writers fence final ownership; duplicate callers join the exact generation.
func (t *inflightTurn) requestStop(ctx context.Context) control.StopOutcome {
	for {
		t.phaseMu.Lock()
		if pending := t.stopPending; pending != nil {
			t.phaseMu.Unlock()
			select {
			case <-pending:
				continue
			case <-ctx.Done():
				return control.StopFailed
			}
		}
		if t.stopErr != nil {
			t.phaseMu.Unlock()
			return control.StopFailed
		}
		select {
		case <-t.stopped:
			t.phaseMu.Unlock()
			return control.StopAlreadyRequested
		default:
		}
		if t.finalizing || t.retired {
			t.phaseMu.Unlock()
			return control.StopFinished
		}
		pending := make(chan struct{})
		t.stopPending = pending
		t.phaseMu.Unlock()
		t.previewMu.Lock() // join issued previews before accepting Stop
		saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		var id string
		var err error
		if t.persistStop != nil {
			id, err = t.persistStop(saveCtx)
		}
		cancel()
		t.phaseMu.Lock()
		if err != nil {
			if errors.Is(err, store.ErrStopPublished) {
				t.finalizing = true
			} else {
				t.stopErr = &stopPersistenceError{err}
				close(t.stopped) // wake sender into a hold, never a successful Stop
			}
		} else {
			t.intentID = id
			close(t.stopped)
		}
		t.stopPending = nil
		close(pending)
		completion := t.completion
		t.phaseMu.Unlock()
		t.previewMu.Unlock()
		if err != nil {
			if errors.Is(err, store.ErrStopPublished) {
				return control.StopFinished
			}
			return control.StopFailed
		}
		t.cancel()
		if completion != nil {
			_ = t.saveCompletion(*completion)
		}
		return control.StopAccepted
	}
}

func (t *inflightTurn) saveCompletion(result runner.RunResult) error {
	if t.persistCompletion == nil {
		return nil
	}
	t.phaseMu.Lock()
	id := t.intentID
	active := id != "" || t.stopErr != nil
	t.phaseMu.Unlock()
	if !active {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := t.persistCompletion(ctx, id, result); err != nil {
		t.phaseMu.Lock()
		t.stopErr = &stopPersistenceError{err}
		t.phaseMu.Unlock()
		return &stopPersistenceError{err}
	}
	return nil
}
func finalGenerationTurn(ctx context.Context) *inflightTurn {
	binding, _ := ctx.Value(finalGenerationKey{}).(finalGeneration)
	return binding.turn
}
func stopFailure(ctx context.Context) error {
	t := finalGenerationTurn(ctx)
	if t == nil {
		return nil
	}
	t.phaseMu.Lock()
	defer t.phaseMu.Unlock()
	return t.stopErr
}
func (b *Bot) observeStopCompletion(ctx context.Context, result runner.RunResult) error {
	t := finalGenerationTurn(ctx)
	if t == nil {
		return nil
	}
	t.phaseMu.Lock()
	t.completion = &result
	pending := t.stopPending
	t.phaseMu.Unlock()
	if pending != nil {
		<-pending
	}
	err := t.saveCompletion(result)
	if err != nil {
		b.log.Error("persist cancelled completion failed", "err", err)
	}
	return err
}

func generationStopped(ctx context.Context) bool {
	t := finalGenerationTurn(ctx)
	if t == nil {
		return false
	}
	select {
	case <-t.stopped:
		return true
	default:
		return false
	}
}

type stopIntentStore interface {
	RecordOrdinaryStop(context.Context, store.OrdinaryStopBinding) (string, error)
	FindOrdinaryStop(context.Context, store.OrdinaryStopBinding) (string, bool, error)
}

func recordStopIntent(ctx context.Context, st stopIntentStore, binding store.OrdinaryStopBinding) (string, error) {
	id, err := st.RecordOrdinaryStop(ctx, binding)
	if err == nil {
		return id, nil
	}
	readCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if found, yes, e := st.FindOrdinaryStop(readCtx, binding); e == nil && yes {
		return found, nil
	}
	return "", err
}

var errPreviewSealed = errors.New("telegram: generation previews sealed")

func withGenerationPreview(ctx context.Context, send func() error) error {
	t := finalGenerationTurn(ctx)
	if t == nil {
		return send()
	}
	t.previewMu.RLock()
	defer t.previewMu.RUnlock()
	t.phaseMu.Lock()
	sealed := t.retired || t.finalizing || t.stopPending != nil || t.stopErr != nil
	select {
	case <-t.stopped:
		sealed = true
	default:
	}
	t.phaseMu.Unlock()
	if sealed {
		return errPreviewSealed
	}
	return send()
}

// Fallback is another native invocation, not terminal presentation. A pending
// or failed Stop must be resolved before it can be admitted.
func generationRunError(ctx context.Context) error {
	t := finalGenerationTurn(ctx)
	if t == nil {
		return nil
	}
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
	if t.retired || t.finalizing {
		return finalOwnershipLost{}
	}
	select {
	case <-t.stopped:
		return queue.ErrStopped
	default:
		return nil
	}
}
