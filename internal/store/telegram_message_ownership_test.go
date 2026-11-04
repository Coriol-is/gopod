package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func TestTelegramOwnershipCrashAndRegistrationIdentity(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := ownerScope(t, s)
	o, err := s.BeginTelegramMessageOwner(ctx, 1, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	// ownerScope uses its own chat identity; use a fixture-independent recipient.
	if o.AttemptID == "" {
		t.Fatal("missing registered attempt", ref)
	}
	d := beginTopic(t, s, ref, 7)
	if err = s.TopicMessageOwnershipIdle(ctx, d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.TopicMessageOwnershipIdle(ctx, d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatalf("restart lost pending %v", err)
	}
	// Receipt is accepted after a deletion intent: it records an issued request,
	// without authorizing a new send or reopening the conversation.
	if err = s.AcknowledgeTelegramMessageOwner(ctx, o, 300); err != nil {
		t.Fatal(err)
	}
	if err = s.TopicMessageOwnershipIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	got, err := s.TelegramMessageOwner(ctx, 1, 300)
	if err != nil || got.SourceThread != 7 || got.TargetThread != 0 || got.Scope != ref {
		t.Fatalf("owner %+v %v", got, err)
	}
	if err = s.AcknowledgeTelegramMessageOwner(ctx, o, 301); !errors.Is(err, ErrMessageOwnership) {
		t.Fatalf("rebound receipt %v", err)
	}
	// Changing registration cannot launder old controls into current authority.
	if err = s.RegisterChat(ctx, RegisteredChat{JID: ref.JID, Folder: ref.Folder, IsOwner: true, AddedAt: 2, Name: "changed"}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TelegramMessageOwner(ctx, 1, 300); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale owner %v", err)
	}
}

func TestTelegramOwnershipRejectsRebindingAndClosedAdmission(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	ref := ownerScope(t, s)
	first, err := s.BeginTelegramMessageOwner(ctx, 1, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.BeginTelegramMessageOwner(ctx, 1, 8, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgeTelegramMessageOwner(ctx, first, 42); err != nil {
		t.Fatal(err)
	}
	if err = s.AcknowledgeTelegramMessageOwner(ctx, second, 42); err == nil {
		t.Fatal("message rebound")
	}
	d := beginTopic(t, s, ref, 8)
	if err = s.TopicMessageOwnershipIdle(ctx, d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal("conflict lost evidence", err)
	}
	if _, err = s.BeginTelegramMessageOwner(ctx, 1, 8, 0); !errors.Is(err, ErrTopicDeleting) {
		t.Fatal("late send admitted", err)
	}
	if err = s.RefuseTelegramMessageOwner(ctx, second.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err = s.TopicMessageOwnershipIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	// An acknowledgement cannot be erased through the refusal API.
	if err = s.RefuseTelegramMessageOwner(ctx, first.AttemptID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.TelegramMessageOwner(ctx, 1, 42); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
	blocked, err := s.UnknownMainControlBlocked(ctx, 1)
	if err != nil || !blocked {
		t.Fatalf("retired unknown admitted %v %v", blocked, err)
	}
}
