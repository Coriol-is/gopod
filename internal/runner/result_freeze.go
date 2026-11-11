package runner

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// ResultFreezer commits the final text and interactive continuity atomically.
// The controller supplies it explicitly; a nil freezer never enables freezing.
type ResultFreezer interface {
	FreezeTurnResult(context.Context, store.ExecutionRef, string) (store.TurnResult, error)
}

type resultFreezeKey struct{}
type resultFreeze struct {
	ref     store.ExecutionRef
	freezer ResultFreezer
}

// WithResultFreeze binds trusted controller-owned attempt membership to one
// interactive run. Provider/model are filled from the selected runtime. The
// caller must fence recovery against frozen results before enabling this path;
// this seam alone does not implement publication or crash recovery.
func WithResultFreeze(ctx context.Context, ref store.ExecutionRef, freezer ResultFreezer) context.Context {
	ref.Members = slices.Clone(ref.Members)
	return context.WithValue(ctx, resultFreezeKey{}, resultFreeze{ref, freezer})
}

var ErrInvalidResultFreeze = errors.New("runner: invalid result freeze binding")

// ResultFreezeError means native execution already completed, but committing
// its result failed. Never rerun the model automatically, even on shutdown.
// Recovery must reconcile the attempt; a failed commit is not a durable result.
type ResultFreezeError struct{ Err error }

func (e *ResultFreezeError) Error() string {
	return fmt.Sprintf("runner: result freeze failed after execution: %v", e.Err)
}
func (e *ResultFreezeError) Unwrap() error  { return e.Err }
func (e *ResultFreezeError) NoReplay() bool { return true }

func validateResultFreeze(ctx context.Context, folder string, thread int) error {
	f, ok := ctx.Value(resultFreezeKey{}).(resultFreeze)
	if !ok {
		return nil
	}
	if !store.ValidScopeIdentity(f.ref.Conversation.Scope) || thread < 0 || (f.ref.Conversation.Visibility != store.VisibilityConversation && f.ref.Conversation.Visibility != store.VisibilityPrivate) || f.freezer == nil || f.ref.Kind != store.ExecInteractive || f.ref.Conversation.Scope.Folder != folder || f.ref.Conversation.ThreadID != thread || len(f.ref.Members) == 0 {
		return ErrInvalidResultFreeze
	}
	seen := map[store.ResultMember]bool{}
	for _, m := range f.ref.Members {
		if m.TurnID <= 0 || m.Attempt <= 0 || seen[m] {
			return ErrInvalidResultFreeze
		}
		seen[m] = true
	}
	return nil
}

func (r *Runner) completeResult(ctx context.Context, folder string, thread int, provider, text string) error {
	f, ok := ctx.Value(resultFreezeKey{}).(resultFreeze)
	if !ok {
		return r.markConversationProvider(ctx, folder, thread, provider, text)
	}
	f.ref.Provider = provider
	saveCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	if _, err := f.freezer.FreezeTurnResult(saveCtx, f.ref, text); err != nil {
		return &ResultFreezeError{Err: err}
	}
	return nil
}

// freezeModel captures the same override that builds the native command. A
// setting change during the run only affects the next execution.
func freezeModel(ctx context.Context, model string) context.Context {
	f, ok := ctx.Value(resultFreezeKey{}).(resultFreeze)
	if !ok {
		return ctx
	}
	f.ref.Model = model
	return context.WithValue(ctx, resultFreezeKey{}, f)
}

// validateResultSelection joins the controller's captured execution identity with
// the provider snapshot resolved under the runner slot. A post-execution freeze
// failure is too late to reject a replacement registration or different batch.
func validateResultSelection(ctx context.Context, selected store.ConversationSelection) error {
	f, frozen := ctx.Value(resultFreezeKey{}).(resultFreeze)
	if !frozen {
		return nil
	}
	if f.ref.Conversation.Scope != selected.Scope || f.ref.Conversation.ThreadID != selected.ThreadID {
		return store.ErrStaleScope
	}
	members, ok := ctx.Value(selectionAttemptsKey{}).([]store.ResultMember)
	if !ok || len(members) != len(f.ref.Members) {
		return ErrInvalidResultFreeze
	}
	want := make(map[store.ResultMember]bool, len(f.ref.Members))
	for _, m := range f.ref.Members {
		want[m] = true
	}
	for _, m := range members {
		if !want[m] {
			return ErrInvalidResultFreeze
		}
		delete(want, m)
	}
	if len(want) != 0 {
		return ErrInvalidResultFreeze
	}
	return nil
}
