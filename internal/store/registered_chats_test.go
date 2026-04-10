package store

import (
	"context"
	"errors"
	"testing"
)

func TestRegisterChatHappyPath(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rc := RegisteredChat{
		JID:             "tg:123",
		Name:            "Anton",
		Folder:          "owner",
		TriggerPattern:  `(?i)@picoclaw\b`,
		RequiresTrigger: true,
		IsOwner:         true,
		AddedAt:         1700000000000,
	}
	if err := s.RegisterChat(ctx, rc); err != nil {
		t.Fatalf("RegisterChat: %v", err)
	}

	got, err := s.GetRegistered(ctx, "tg:123")
	if err != nil {
		t.Fatalf("GetRegistered: %v", err)
	}
	if got.Folder != "owner" || !got.IsOwner || !got.RequiresTrigger {
		t.Errorf("got %+v", got)
	}
	if got.TriggerPattern != `(?i)@picoclaw\b` {
		t.Errorf("trigger pattern lost: %q", got.TriggerPattern)
	}
}

func TestRegisterChatIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	rc := RegisteredChat{
		JID: "tg:123", Name: "Old Name", Folder: "owner",
		IsOwner: true, AddedAt: 1700000000000,
	}
	if err := s.RegisterChat(ctx, rc); err != nil {
		t.Fatalf("first: %v", err)
	}

	rc.Name = "New Name"
	rc.AddedAt = 1700000999999 // a later AddedAt should NOT overwrite
	if err := s.RegisterChat(ctx, rc); err != nil {
		t.Fatalf("second: %v", err)
	}

	got, _ := s.GetRegistered(ctx, "tg:123")
	if got.Name != "New Name" {
		t.Errorf("name = %q, want New Name", got.Name)
	}
	if got.AddedAt != 1700000000000 {
		t.Errorf("added_at regressed: %d, want %d (preserved on update)", got.AddedAt, 1700000000000)
	}
}

func TestGetRegisteredNotFound(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	_, err := s.GetRegistered(ctx, "tg:does-not-exist")
	if !errors.Is(err, ErrChatNotRegistered) {
		t.Errorf("err = %v, want ErrChatNotRegistered", err)
	}
}

func TestIsRegistered(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	yes, err := s.IsRegistered(ctx, "tg:123")
	if err != nil || yes {
		t.Errorf("unregistered: yes=%v err=%v", yes, err)
	}

	if err := s.RegisterChat(ctx, RegisteredChat{
		JID: "tg:123", Folder: "owner", IsOwner: true, AddedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	yes, err = s.IsRegistered(ctx, "tg:123")
	if err != nil || !yes {
		t.Errorf("registered: yes=%v err=%v", yes, err)
	}
}

func TestListRegisteredOrder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	// Register out of timestamp order on purpose.
	chats := []RegisteredChat{
		{JID: "tg:b", Folder: "two", AddedAt: 200},
		{JID: "tg:a", Folder: "one", AddedAt: 100},
		{JID: "tg:c", Folder: "three", AddedAt: 300},
	}
	for _, c := range chats {
		if err := s.RegisterChat(ctx, c); err != nil {
			t.Fatalf("register %s: %v", c.JID, err)
		}
	}

	got, err := s.ListRegistered(ctx)
	if err != nil {
		t.Fatalf("ListRegistered: %v", err)
	}
	wantOrder := []string{"tg:a", "tg:b", "tg:c"}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	for i, w := range wantOrder {
		if got[i].JID != w {
			t.Errorf("got[%d].JID = %q, want %q", i, got[i].JID, w)
		}
	}
}

func TestRegisterChatRejectsBadFolder(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	cases := []string{"", "../etc", "has space", "../../etc/passwd", "a/b"}
	for _, folder := range cases {
		err := s.RegisterChat(ctx, RegisteredChat{
			JID: "tg:1", Folder: folder, AddedAt: 1,
		})
		if err == nil {
			t.Errorf("folder %q was accepted, want error", folder)
		}
	}
}

func TestRegisterChatRejectsEmptyJID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if err := s.RegisterChat(ctx, RegisteredChat{Folder: "owner", AddedAt: 1}); err == nil {
		t.Error("empty JID was accepted")
	}
}
