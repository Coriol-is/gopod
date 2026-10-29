package store

import (
	"context"
	"errors"
	"testing"
)

func registerForTrigger(t *testing.T, s *Store, jid, folder string, owner bool) {
	t.Helper()
	if err := s.RegisterChat(context.Background(), RegisteredChat{JID: jid, Folder: folder, IsOwner: owner, AddedAt: 1700000000000}); err != nil {
		t.Fatal(err)
	}
}

func TestNewGroupGetsMentionAndLegacyRowsGetAll(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	registerForTrigger(t, s, "tg:-100123", "grp", false)
	if got, err := s.GroupTrigger(ctx, "tg:-100123"); err != nil || got != TriggerMention {
		t.Fatalf("new group = %q, %v; want mention", got, err)
	}
	// A registration made before D048 has no row: it keeps today's behavior.
	registerForTrigger(t, s, "tg:-100456", "old", false)
	if _, err := s.db.ExecContext(ctx, `DELETE FROM group_triggers WHERE jid='tg:-100456'`); err != nil {
		t.Fatal(err)
	}
	if got, err := s.GroupTrigger(ctx, "tg:-100456"); err != nil || got != TriggerAll {
		t.Fatalf("legacy group = %q, %v; want all", got, err)
	}
	// Private chats are never seeded.
	registerForTrigger(t, s, "tg:77", "owner", true)
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM group_triggers WHERE jid='tg:77'`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("private seeded: %d %v", n, err)
	}
}

func TestSetGroupTriggerValidatesAndSurvivesEpochRotation(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	registerForTrigger(t, s, "tg:-100123", "grp", false)
	if err := s.SetGroupTrigger(ctx, "tg:-100123", TriggerMode("bogus"), 1); !errors.Is(err, ErrInvalidTriggerMode) {
		t.Fatalf("bogus mode: %v", err)
	}
	if err := s.SetGroupTrigger(ctx, "tg:-999", TriggerAll, 1); !errors.Is(err, ErrChatNotRegistered) {
		t.Fatalf("unregistered: %v", err)
	}
	if err := s.SetGroupTrigger(ctx, "tg:-100123", TriggerOwnerMention, 2); err != nil {
		t.Fatal(err)
	}
	rc, err := s.GetRegistered(ctx, "tg:-100123")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.RotateGroupEpoch(ctx, rc.JID, rc.RegistrationRevision); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.GroupTrigger(ctx, "tg:-100123"); got != TriggerOwnerMention {
		t.Fatalf("after rotation = %q", got)
	}
}

func TestParseTriggerMode(t *testing.T) {
	for in, want := range map[string]TriggerMode{"OWNER": TriggerOwner, " mention ": TriggerMention, "owner+mention": TriggerOwnerMention, "all": TriggerAll} {
		if got, err := ParseTriggerMode(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, in := range []string{"", "owner mention", "everyone"} {
		if _, err := ParseTriggerMode(in); !errors.Is(err, ErrInvalidTriggerMode) {
			t.Errorf("%q accepted", in)
		}
	}
}
