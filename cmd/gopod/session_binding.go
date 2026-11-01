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
}

// bindingHandler wraps next so every legacy queue batch is bound to the
// conversation's active session and context epoch and holds an execution claim
// while it runs. Managed task attempts pass through: the CP-004 ledger owns them.
func bindingHandler(st sessionBinder, provider, model func(folder string) string, next queue.Handler, log *slog.Logger) queue.Handler {
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
		if err := st.AcquireExecutionClaim(ctx, scope, b.ActiveSessionID, last); err != nil {
			return fmt.Errorf("claim session %s for turn %d: %w", b.ActiveSessionID, last, err)
		}
		runErr := next(ctx, items)
		sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), claimSettleTimeout)
		defer cancel()
		unknown := errors.Is(runErr, runner.ErrExecutionUnproven)
		if err := st.SettleExecutionClaim(sctx, b.ActiveSessionID, last, unknown); err != nil {
			log.Warn("settle execution claim failed", "session", b.ActiveSessionID, "turn", last, "unknown", unknown, "err", err)
		}
		return runErr
	}
}
