package store

import (
	"context"
	"path/filepath"
	"testing"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(context.Background(), filepath.Join(dir, "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSaveMessage(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.SaveMessage(ctx, Message{
		ChatJID:     "tg:123",
		TGMessageID: 42,
		Sender:      "1001",
		SenderName:  "alice",
		Content:     "hello",
		Timestamp:   1700000000000,
	})
	if err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}
	if id == 0 {
		t.Fatal("SaveMessage returned id=0 for new row")
	}

	n, err := s.CountMessages(ctx)
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	if n != 1 {
		t.Errorf("CountMessages = %d, want 1", n)
	}
}

func TestSaveMessageIdempotent(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	m := Message{
		ChatJID:     "tg:123",
		TGMessageID: 42,
		Sender:      "1001",
		Content:     "hello",
		Timestamp:   1700000000000,
	}

	if _, err := s.SaveMessage(ctx, m); err != nil {
		t.Fatalf("first SaveMessage: %v", err)
	}
	// Re-deliver the same Telegram update.
	if _, err := s.SaveMessage(ctx, m); err != nil {
		t.Fatalf("second SaveMessage (should be no-op): %v", err)
	}

	n, _ := s.CountMessages(ctx)
	if n != 1 {
		t.Errorf("after duplicate insert, CountMessages = %d, want 1", n)
	}
}

func TestSaveMessageRejectsEmptyJID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.SaveMessage(ctx, Message{TGMessageID: 1, Content: "x"}); err == nil {
		t.Fatal("want error for empty ChatJID")
	}
}

func TestSaveMessageRejectsZeroID(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)
	if _, err := s.SaveMessage(ctx, Message{ChatJID: "tg:1", Content: "x"}); err == nil {
		t.Fatal("want error for zero TGMessageID")
	}
}

func TestSaveMessageReplyFields(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	id, err := s.SaveMessage(ctx, Message{
		ChatJID:            "tg:123",
		TGMessageID:        43,
		Sender:             "1001",
		SenderName:         "alice",
		Content:            "yes",
		Timestamp:          1700000001000,
		ReplyToTGMessageID: 42,
		ReplyToContent:     "you sure?",
		ReplyToSenderName:  "bob",
	})
	if err != nil {
		t.Fatalf("SaveMessage: %v", err)
	}

	// Spot-check that the columns persisted (not NULL).
	var rcontent, rname string
	var rid int64
	err = s.DB().QueryRowContext(ctx,
		`SELECT reply_to_tg_message_id, reply_to_content, reply_to_sender_name FROM messages WHERE id=?`,
		id).Scan(&rid, &rcontent, &rname)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if rid != 42 || rcontent != "you sure?" || rname != "bob" {
		t.Errorf("reply fields = (%d, %q, %q), want (42, %q, %q)", rid, rcontent, rname, "you sure?", "bob")
	}
}

func TestUpsertChat(t *testing.T) {
	ctx := context.Background()
	s := newTestStore(t)

	if err := s.UpsertChat(ctx, ChatRecord{
		JID: "tg:123", Name: "Alice", LastMessageTime: 1700000000000, IsGroup: false,
	}); err != nil {
		t.Fatalf("first UpsertChat: %v", err)
	}

	// Newer message; name updated, last_message_time advanced.
	if err := s.UpsertChat(ctx, ChatRecord{
		JID: "tg:123", Name: "Alice Smith", LastMessageTime: 1700000060000, IsGroup: false,
	}); err != nil {
		t.Fatalf("second UpsertChat: %v", err)
	}

	// Older message; should NOT regress last_message_time.
	if err := s.UpsertChat(ctx, ChatRecord{
		JID: "tg:123", Name: "Alice Smith", LastMessageTime: 1699999999999, IsGroup: false,
	}); err != nil {
		t.Fatalf("third UpsertChat: %v", err)
	}

	n, _ := s.CountChats(ctx)
	if n != 1 {
		t.Errorf("CountChats = %d, want 1 (upserts must not insert duplicates)", n)
	}

	var name string
	var lastMs int64
	err := s.DB().QueryRowContext(ctx,
		`SELECT name, last_message_time FROM chats WHERE jid=?`, "tg:123").Scan(&name, &lastMs)
	if err != nil {
		t.Fatalf("scan: %v", err)
	}
	if name != "Alice Smith" {
		t.Errorf("name = %q, want %q", name, "Alice Smith")
	}
	if lastMs != 1700000060000 {
		t.Errorf("last_message_time = %d, want 1700000060000 (max of all upserts)", lastMs)
	}
}
