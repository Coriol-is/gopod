package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func topicDeliveryFixture(t *testing.T) *deliveryFixture {
	f := newDeliveryFixture(t)
	var err error
	f.conv, err = f.st.SnapshotConversation(t.Context(), "tg:1", 7, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestTopicDeletionBlocksNewPublicationAndValidation(t *testing.T) {
	for _, state := range []string{"deleting", "uncertain", "deleted", "retired"} {
		t.Run(state, func(t *testing.T) {
			f := topicDeliveryFixture(t)
			d := beginTopic(t, f.st, f.conv.Scope, 7)
			if state != "deleting" {
				outcome := state
				if state == "retired" {
					outcome = "deleted"
				}
				if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, outcome); err != nil {
					t.Fatal(err)
				}
			}
			if state == "retired" {
				if err := f.st.RetireDeletedTopic(t.Context(), d); err != nil {
					t.Fatal(err)
				}
			}
			want := ErrTopicDeleting
			if state == "deleted" || state == "retired" {
				want = ErrConversationRetired
			}
			if err := f.st.ValidateConversation(t.Context(), f.conv); !errors.Is(err, want) {
				t.Fatalf("validation: %v", err)
			}
			if _, err := f.st.PublishDelivery(t.Context(), f.publishReq("", "late")); !errors.Is(err, want) {
				t.Fatalf("publish: %v", err)
			}
			if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 0 {
				t.Fatal("refused publication left a group")
			}
		})
	}
}

func TestUnresolvedTopicHoldsOutboxWithoutBlockingOtherTopics(t *testing.T) {
	for _, state := range []string{"deleting", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			f := topicDeliveryFixture(t)
			o := f.owner()
			held := f.publish("held")
			d := beginTopic(t, f.st, f.conv.Scope, 7)
			if state == "uncertain" {
				if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "uncertain"); err != nil {
					t.Fatal(err)
				}
			}
			for _, thread := range []int{0, 8} {
				req := f.publishReq("", "other")
				req.Conversation.ThreadID = thread
				if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
					t.Fatal(err)
				}
				c := mustClaim(t, o, time.Now())
				if c.Conversation.ThreadID != thread {
					t.Fatalf("claimed deleting topic: %+v", c)
				}
				if err := o.AckPart(t.Context(), c, int64(100+thread)); err != nil {
					t.Fatal(err)
				}
			}
			noClaim(t, o, time.Now())
			diag := mustDiag(t, f, held.ID)
			if diag.Parts[0].State != PartPending {
				t.Fatalf("held part lost: %+v", diag)
			}
			if diag.Parts[0].Attempts != 0 {
				t.Fatal("held delivery consumed an attempt")
			}
			if state == "deleting" {
				if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
					t.Fatal(err)
				}
				c := mustClaim(t, o, time.Now())
				if c.DeliveryID != held.ID || c.Attempt != 1 {
					t.Fatalf("refusal did not restore original delivery: %+v", c)
				}
			} else {
				if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "refused"); !errors.Is(err, ErrTopicDeletionState) {
					t.Fatalf("uncertainty lifted: %v", err)
				}
				noClaim(t, o, time.Now())
			}
		})
	}
}

func TestConfirmedTopicDeletionCancelsOnlyPendingParts(t *testing.T) {
	f := topicDeliveryFixture(t)
	o := f.owner()
	delivery := f.publish("first", "second")
	c := mustClaim(t, o, time.Now())
	d := beginTopic(t, f.st, f.conv.Scope, 7)
	if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "deleted"); err != nil {
		t.Fatal(err)
	}
	// A request issued before deletion can still be acknowledged honestly. Never
	// rewrite a remote acknowledgement into cancellation or resend it into main.
	if err := o.AckPart(t.Context(), c, 123); err != nil {
		t.Fatal(err)
	}
	noClaim(t, o, time.Now())
	diag := mustDiag(t, f, delivery.ID)
	if diag.Parts[0].State != PartSent || diag.Parts[1].State != PartCancelled || diag.Parts[1].ReasonCode != "topic_retired" {
		t.Fatalf("wrong settlement: %+v", diag)
	}
	if countRows(t, f.st, `SELECT count(*) FROM outbox_attempts`) != 1 {
		t.Fatal("late part acquired an attempt")
	}
}

func TestTopicDeletionFencesPlainFallbackWithoutLosingAttempt(t *testing.T) {
	f := topicDeliveryFixture(t)
	o := f.owner()
	f.publish("<b>text</b>")
	c := mustClaim(t, o, time.Now())
	beginTopic(t, f.st, f.conv.Scope, 7)
	if _, err := o.BeginPlainFallback(t.Context(), c); !errors.Is(err, ErrTopicDeleting) {
		t.Fatalf("fallback accepted: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM outbox_attempts`) != 1 || countRows(t, f.st, `SELECT count(*) FROM outbox_plain_fallbacks`) != 0 {
		t.Fatal("new fallback attempt created")
	}
	if err := o.MarkUncertain(t.Context(), c, "timeout", "late result unknown"); err != nil {
		t.Fatal(err)
	}
	noClaim(t, o, time.Now())
}

func TestTopicOutboxHoldSurvivesStoreReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		s.Close()
		t.Fatal(err)
	}
	conv, err := s.SnapshotConversation(t.Context(), "tg:1", 7, VisibilityConversation)
	if err != nil {
		s.Close()
		t.Fatal(err)
	}
	f := &deliveryFixture{t: t, st: s, conv: conv}
	delivery := f.publish("durable hold")
	d := beginTopic(t, s, conv.Scope, 7)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	o, err := s.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Release()
	noClaim(t, o, time.Now())
	if err := s.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if c.DeliveryID != delivery.ID || c.Attempt != 1 {
		t.Fatalf("lost/replayed delivery: %+v", c)
	}
}

func TestDeletedTopicCancelsUndispatchablePendingParts(t *testing.T) {
	for _, head := range []string{"sending", "uncertain", "delayed"} {
		t.Run(head, func(t *testing.T) {
			f := topicDeliveryFixture(t)
			o := f.owner()
			delivery := f.publish("first", "second")
			c := mustClaim(t, o, time.Now())
			switch head {
			case "uncertain":
				if err := o.MarkUncertain(t.Context(), c, "timeout", "class=ambiguous code=timeout"); err != nil {
					t.Fatal(err)
				}
			case "delayed":
				if _, err := o.ReleaseUnsent(t.Context(), c, time.Now().Add(time.Hour), "retry", "class=rate_limited code=retry", false); err != nil {
					t.Fatal(err)
				}
			}
			d := beginTopic(t, f.st, f.conv.Scope, 7)
			if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "deleted"); err != nil {
				t.Fatal(err)
			}
			noClaim(t, o, time.Now())
			diag := mustDiag(t, f, delivery.ID)
			want := head
			if head == "delayed" {
				want = PartCancelled
			}
			if diag.Parts[0].State != want || diag.Parts[1].State != PartCancelled || diag.Parts[1].ReasonCode != "topic_retired" {
				t.Fatalf("blocked tail not settled: %+v", diag)
			}
			if head == "sending" {
				if err := o.AckPart(t.Context(), c, 123); err != nil {
					t.Fatalf("late ack lost: %v", err)
				}
			}
		})
	}
}
