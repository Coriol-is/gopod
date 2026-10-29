package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// turnToItem maps a durable turn row to a queue item, including the managed
// task links read from the ledger.
func turnToItem(t store.Turn) queue.Item {
	return queue.Item{
		ID:               t.ID,
		Source:           t.Source,
		SourceID:         t.SourceID,
		Attempts:         t.Attempts,
		PlaceholderMsgID: t.PlaceholderMsgID,
		ReplyMsgID:       t.ReplyMsgID,
		ChatID:           t.ChatID,
		MessageID:        int(t.TGMessageID),
		ThreadID:         t.ThreadID,
		Folder:           t.ChatFolder,
		IsOwner:          t.IsOwner,
		Text:             t.Text,
		FilePath:         t.FilePath,
		IsVoice:          t.IsVoice,
		RunID:            t.RunID,
		AttemptID:        t.AttemptID,
		SessionKind:      t.SessionKind,
	}
}

// managedLedger adapts *store.Store to queue.ManagedStore. It holds no state:
// every decision is a committed ledger transaction.
type managedLedger struct{ st *store.Store }

func (m managedLedger) LoadTurn(ctx context.Context, id int64) (queue.Item, error) {
	t, err := m.st.GetTurn(ctx, id)
	if err != nil {
		return queue.Item{}, err
	}
	return turnToItem(t), nil
}

func (m managedLedger) Reserve(ctx context.Context, attemptID string, snap queue.Snapshot, now time.Time) (queue.ReserveOutcome, error) {
	res, err := m.st.ReserveTaskAttempt(ctx, attemptID, store.DispatchSnapshot{
		Provider: snap.Provider, Model: snap.Model, ProfileFingerprint: snap.ProfileFingerprint,
	}, now.UnixMilli())
	if err != nil {
		return queue.ReserveOutcome{}, err
	}
	return queue.ReserveOutcome{
		Kind:   queue.ReserveKind(res.Kind),
		Reason: res.Reason,
		Snapshot: queue.Snapshot{
			Provider: res.Attempt.Provider, Model: res.Attempt.Model, ProfileFingerprint: res.Attempt.ProfileFingerprint,
		},
	}, nil
}

// BeginSpawn maps only the ledger's compare-and-set refusal to ErrSpawnRefused:
// that is the one case where the marker is proven not committed.
func (m managedLedger) BeginSpawn(ctx context.Context, attemptID string) error {
	err := m.st.BeginTaskSpawn(ctx, attemptID)
	if errors.Is(err, store.ErrTransitionConflict) {
		return fmt.Errorf("%w: %v", queue.ErrSpawnRefused, err)
	}
	return err
}

func (m managedLedger) Settle(ctx context.Context, attemptID string, s queue.Settlement) (string, error) {
	state, err := m.st.SettleTaskAttempt(ctx, attemptID, store.Settlement{
		Kind: store.SettleKind(s.Kind), Evidence: s.Evidence, FailureCode: s.FailureCode,
	})
	return string(state), err
}

func (m managedLedger) Recover(ctx context.Context, turnID int64) (queue.Recovery, error) {
	plan, err := m.st.RecoverTaskAttempt(ctx, turnID)
	if err != nil {
		return queue.Recovery{}, err
	}
	return queue.Recovery{Action: queue.RecoveryAction(plan.Action), State: string(plan.Attempt.State), ResultID: plan.ResultID}, nil
}

func (m managedLedger) Held(ctx context.Context) ([]queue.Hold, error) {
	held, err := m.st.HeldTaskReservations(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]queue.Hold, 0, len(held))
	for _, h := range held {
		out = append(out, queue.Hold{AttemptID: h.AttemptID, RunID: h.RunID, Folder: h.Folder, TurnID: h.TurnID})
	}
	return out, nil
}

func (m managedLedger) Release(ctx context.Context, attemptID string, proof queue.ReleaseProof, detail string) error {
	return m.st.ReleaseAttemptReservation(ctx, attemptID, store.ReleaseProof(proof), detail)
}
