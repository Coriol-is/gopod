package store

import (
	"errors"
	"testing"
)

func TestTelegramTopicOwnershipAdmissionAndReceiptBinding(t *testing.T) {
	s := openTestStore(t)
	ref := ownerScope(t, s)
	for _, route := range [][2]int{{7, 8}, {0, 7}, {7, -1}, {-1, 0}} {
		if _, err := s.BeginTelegramMessageOwner(t.Context(), 1, route[0], route[1]); !errors.Is(err, ErrInvalidConversation) {
			t.Fatalf("route %v: %v", route, err)
		}
	}
	o, err := s.BeginTelegramMessageOwner(t.Context(), 1, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	changed := o
	changed.TargetThread = 0
	if err := s.AcknowledgeTelegramMessageOwner(t.Context(), changed, 71); !errors.Is(err, ErrMessageOwnership) {
		t.Fatalf("moved ACK target: %v", err)
	}
	if err := s.ReconcileTelegramMessageOwner(t.Context(), changed, 71); !errors.Is(err, ErrMessageOwnership) {
		t.Fatalf("moved repair target: %v", err)
	}
	d := beginTopic(t, s, ref, 7)
	if _, err := s.BeginTelegramMessageOwner(t.Context(), 1, 7, 7); !errors.Is(err, ErrTopicDeleting) {
		t.Fatalf("admitted deleting topic: %v", err)
	}
	if err := s.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatalf("missing hold: %v", err)
	}
	if err := s.ReconcileTelegramMessageOwner(t.Context(), o, 71); err != nil {
		t.Fatal(err)
	}
	got, err := s.TelegramMessageOwner(t.Context(), 1, 71)
	if err != nil || got.TargetThread != 7 || got.SourceThread != 7 {
		t.Fatalf("owner %+v: %v", got, err)
	}
	if err := s.TopicMessageOwnershipIdle(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	sibling, err := s.BeginTelegramMessageOwner(t.Context(), 1, 8, 8)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AcknowledgeTelegramMessageOwner(t.Context(), sibling, 71); err == nil {
		t.Fatal("same message rebound to sibling topic")
	}
	if _, err := s.BeginTelegramMessageOwner(t.Context(), 1, 0, 0); err != nil {
		t.Fatalf("main blocked: %v", err)
	}
}
