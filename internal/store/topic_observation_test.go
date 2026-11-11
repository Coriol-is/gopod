package store

import (
	"errors"
	"testing"
)

func TestTopicDeletedObservationAtomicScopedAndIdempotent(t *testing.T) {
	s := openTestStore(t)
	ctx := t.Context()
	ref := ownerScope(t, s)
	for _, thread := range []int{0, 7, 8} {
		if err := s.PutSession(ctx, "owner", thread, "claude", "native"); err != nil {
			t.Fatal(err)
		}
	}
	owner, err := s.BeginTelegramMessageOwner(ctx, 1, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `CREATE TRIGGER reject_observation BEFORE INSERT ON topic_deletions BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ObserveDeletedTelegramTopic(ctx, owner); err == nil {
		t.Fatal("fault accepted")
	}
	var count int
	if err = s.db.QueryRowContext(ctx, `SELECT count(*) FROM telegram_message_ownership WHERE attempt_id=?`, owner.AttemptID).Scan(&count); err != nil || count != 1 {
		t.Fatal("atomic refusal lost evidence", count, err)
	}
	if _, err = s.db.ExecContext(ctx, `DROP TRIGGER reject_observation`); err != nil {
		t.Fatal(err)
	}
	d, err := s.ObserveDeletedTelegramTopic(ctx, owner)
	if err != nil || d.State != "deleted" {
		t.Fatal(d, err)
	}
	replay, err := s.ObserveDeletedTelegramTopic(ctx, owner)
	if err != nil || replay.IntentID != d.IntentID {
		t.Fatal(replay, err)
	}
	if err = s.ClaimTopicDeletionDispatch(ctx, d); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal("observation gained dispatch", err)
	}
	if err = s.TopicMessageOwnershipIdle(ctx, d); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
	for _, thread := range []int{0, 8} {
		if _, ok, e := s.GetSession(ctx, "owner", thread, "claude"); e != nil || !ok {
			t.Fatal("sibling lost", thread, e)
		}
	}
	if _, ok, e := s.GetSession(ctx, "owner", 7, "claude"); e != nil || ok {
		t.Fatal("target remains", ok, e)
	}
	if _, found, e := s.GetTopicDeletion(ctx, ref, 7); e != nil || !found {
		t.Fatal(found, e)
	}
}

func TestTopicDeletedObservationRejectsFallbackACKAndRegistrationDrift(t *testing.T) {
	for _, name := range []string{"fallback", "ack", "scope"} {
		t.Run(name, func(t *testing.T) {
			s := openTestStore(t)
			ctx := t.Context()
			ownerScope(t, s)
			target := 7
			if name == "fallback" {
				target = 0
			}
			owner, e := s.BeginTelegramMessageOwner(ctx, 1, 7, target)
			if e != nil {
				t.Fatal(e)
			}
			if name == "ack" {
				if e = s.AcknowledgeTelegramMessageOwner(ctx, owner, 100); e != nil {
					t.Fatal(e)
				}
			}
			if name == "scope" {
				if e = s.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", Name: "changed", IsOwner: true, AddedAt: 1}); e != nil {
					t.Fatal(e)
				}
			}
			if _, e = s.ObserveDeletedTelegramTopic(ctx, owner); e == nil {
				t.Fatal("invalid observation accepted")
			}
			var n int
			if e = s.db.QueryRowContext(ctx, `SELECT count(*) FROM topic_deletions`).Scan(&n); e != nil || n != 0 {
				t.Fatal(n, e)
			}
		})
	}
}
