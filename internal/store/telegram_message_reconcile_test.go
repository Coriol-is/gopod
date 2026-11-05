package store

import (
	"errors"
	"sync"
	"testing"
)

func TestMessageReceiptReconciliationIdentityAndIdempotency(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	ref := ownerScope(t, s)
	owner, err := s.BeginTelegramMessageOwner(ctx, 1, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*TelegramMessageOwner){
		func(o *TelegramMessageOwner) { o.AttemptID = "other" }, func(o *TelegramMessageOwner) { o.ChatID = 2 },
		func(o *TelegramMessageOwner) { o.SourceThread = 8 }, func(o *TelegramMessageOwner) { o.TargetThread = 9 },
		func(o *TelegramMessageOwner) { o.Scope.NamespaceEpoch++ },
	} {
		bad := owner
		change(&bad)
		if err = s.ReconcileTelegramMessageOwner(ctx, bad, 11); err == nil {
			t.Fatal("mismatched receipt accepted")
		}
	}
	d := beginTopic(t, s, ref, 7)
	if err = s.TopicMessageOwnershipIdle(ctx, d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
	// A positive send receipt can settle during deletion, without authorizing a send.
	var wg sync.WaitGroup
	results := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); results <- s.ReconcileTelegramMessageOwner(ctx, owner, 11) }()
	}
	wg.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = s.TopicMessageOwnershipIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	// Commit-then-error retry is idempotent, but a different message cannot replace it.
	if err = s.ReconcileTelegramMessageOwner(ctx, owner, 11); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileTelegramMessageOwner(ctx, owner, 12); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
}
func TestMessageReceiptReconciliationRejectsRegistrationDrift(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	ref := ownerScope(t, s)
	o, err := s.BeginTelegramMessageOwner(ctx, 1, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.RegisterChat(ctx, RegisteredChat{JID: ref.JID, Folder: ref.Folder, IsOwner: true, Name: "changed", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err = s.ReconcileTelegramMessageOwner(ctx, o, 44); !errors.Is(err, ErrStaleScope) {
		t.Fatal(err)
	}
	blocked, err := s.UnknownMainControlBlocked(ctx, 1)
	if err != nil || !blocked {
		t.Fatal("lost stale pending evidence", err)
	}
}
