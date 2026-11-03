package store

import (
	"errors"
	"testing"
	"time"
)

func TestTopicQuiescenceDurableWorkAndIdentityGates(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	ref := ownerScope(t, s)
	binding, err := s.ResolveConversation(ctx, ref, 7)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := s.InsertTurn(ctx, Turn{Source: "telegram", SourceID: "late", ChatFolder: "owner", ThreadID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireExecutionClaim(ctx, ref, binding.ActiveSessionID, id); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, s, ref, 7)
	if err = s.TopicDeletionWorkIdle(ctx, d); !errors.Is(err, ErrSessionBusy) {
		t.Fatal(err)
	}
	if err = s.MarkTurnFinished(ctx, id, TurnStopped, ""); err != nil {
		t.Fatal(err)
	}
	if err = s.TopicDeletionWorkIdle(ctx, d); !errors.Is(err, ErrSessionBusy) {
		t.Fatal("claim ignored", err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM execution_claims WHERE session_id=?`, binding.ActiveSessionID); err != nil {
		t.Fatal(err)
	}
	if err = s.TopicDeletionWorkIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, d, "refused"); err != nil {
		t.Fatal(err)
	}
	next := beginTopic(t, s, ref, 7)
	called := false
	if err = s.WithRefusedTopicFence(ctx, d, func() error { called = true; return nil }); !errors.Is(err, ErrTopicDeletionState) || called {
		t.Fatalf("stale release %v %v", err, called)
	}
	if err = s.TopicDeletionWorkIdle(ctx, next); err != nil {
		t.Fatal(err)
	}
}
func TestTopicQuiescenceIssuedOutboxRemainsBusy(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	var err error
	f.conv, err = f.st.SnapshotConversation(ctx, "tg:1", 7, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	f.publish("topic")
	o := f.owner()
	claim := mustClaim(t, o, time.Now())
	d := beginTopic(t, f.st, f.conv.Scope, 7)
	if err = f.st.TopicDeletionTransportIdle(ctx, d); !errors.Is(err, ErrTopicTransportBusy) {
		t.Fatalf("issued request ignored: %v", err)
	}
	if err = o.AckPart(ctx, claim, 999); err != nil {
		t.Fatal(err)
	}
	if err = f.st.TopicDeletionTransportIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	pending, err := f.st.PendingTopicDeletions(ctx)
	if err != nil || len(pending) != 1 || pending[0].IntentID != d.IntentID {
		t.Fatalf("restore %+v %v", pending, err)
	}
}
