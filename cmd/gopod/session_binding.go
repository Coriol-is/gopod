package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// ErrContextEpochChanged reports a turn bound to a context epoch or session
// that is no longer the conversation's active one (for example after /new).
var ErrContextEpochChanged = errors.New("gopod: turn bound to a superseded context epoch")

// claimSettleTimeout bounds the settle write that runs after the handler.
const claimSettleTimeout = 5 * time.Second

// sessionBinder is the slice of *store.Store the binding handler needs.
type sessionBinder interface {
	SnapshotScope(ctx context.Context, folder string) (store.ScopeRef, error)
	ResolveConversation(ctx context.Context, scope store.ScopeRef, thread int) (store.ConversationBinding, error)
	BindAttempt(ctx context.Context, scope store.ScopeRef, b store.AttemptBinding) error
	GetAttemptBinding(ctx context.Context, turnID int64) (store.AttemptBinding, bool, error)
	AcquireExecutionClaim(ctx context.Context, scope store.ScopeRef, sessionID string, turnID int64) error
	SettleExecutionClaim(ctx context.Context, sessionID string, turnID int64, unknown bool) error
	ExecutionClaimHolder(ctx context.Context, sessionID string) (turnID int64, state string, ok bool, err error)
}

// bindingHandler wraps next so every legacy queue batch is bound to the
// conversation's active session and context epoch and holds an execution claim
// while it runs. Managed task attempts pass through: the CP-004 ledger owns them.
// uncertain is the runner's process proof (Runner.ExecutionUncertain): when it
// reports an unproven exit after the run, the claim is settled
// execution_unknown even if the handler swallowed the runner error.
func bindingHandler(st sessionBinder, provider, model func(folder string) string, uncertain func(ctx context.Context, folder string) bool, next queue.Handler, log *slog.Logger) queue.Handler {
	return func(ctx context.Context, items []queue.Item) error {
		if len(items) == 0 || items[0].Managed() {
			return next(ctx, items)
		}
		folder, thread := items[0].Folder, items[0].ThreadID
		scope, err := st.SnapshotScope(ctx, folder)
		if err != nil {
			return fmt.Errorf("snapshot scope for %q: %w", folder, err)
		}
		b, err := st.ResolveConversation(ctx, scope, thread)
		if err != nil {
			return fmt.Errorf("resolve conversation %q/%d: %w", folder, thread, err)
		}
		prov, mod := provider(folder), model(folder)
		for _, it := range items {
			prev, bound, err := st.GetAttemptBinding(ctx, it.ID)
			if err != nil {
				return fmt.Errorf("read attempt binding for turn %d: %w", it.ID, err)
			}
			if bound && (prev.ContextEpoch != b.ContextEpoch || prev.SessionID != b.ActiveSessionID) {
				log.Info("refusing turn bound to a superseded context epoch", "turn", it.ID,
					"session", prev.SessionID, "epoch", prev.ContextEpoch,
					"active_session", b.ActiveSessionID, "active_epoch", b.ContextEpoch)
				// ErrStopped makes the queue finish the batch without a rerun.
				return fmt.Errorf("%w: %w: turn %d", queue.ErrStopped, ErrContextEpochChanged, it.ID)
			}
			err = st.BindAttempt(ctx, scope, store.AttemptBinding{
				TurnID: it.ID, SessionID: b.ActiveSessionID, ContextEpoch: b.ContextEpoch,
				Provider: prov, Model: mod,
			})
			if err != nil {
				return fmt.Errorf("bind turn %d: %w", it.ID, err)
			}
		}
		last := items[len(items)-1].ID
		prevTurn, prevState, held, err := st.ExecutionClaimHolder(ctx, b.ActiveSessionID)
		if err != nil {
			return fmt.Errorf("read execution claim of session %s: %w", b.ActiveSessionID, err)
		}
		if err := st.AcquireExecutionClaim(ctx, scope, b.ActiveSessionID, last); err != nil {
			return fmt.Errorf("claim session %s for turn %d: %w", b.ActiveSessionID, last, err)
		}
		if held && prevTurn != last {
			log.Warn("execution claim taken over", "session", b.ActiveSessionID, "turn", last,
				"previous_turn", prevTurn, "previous_state", prevState)
		}
		runErr := next(ctx, items)
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimSettleTimeout)
		defer cancel()
		unknown := errors.Is(runErr, runner.ErrExecutionUnproven) || uncertain(sctx, folder)
		if err := st.SettleExecutionClaim(sctx, b.ActiveSessionID, last, unknown); err != nil {
			log.Warn("settle execution claim failed", "session", b.ActiveSessionID, "turn", last, "unknown", unknown, "err", err)
		}
		return runErr
	}
}
