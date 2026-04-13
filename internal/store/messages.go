package store

import (
	"context"
	"errors"
	"fmt"
)

// Message is the gopod representation of one Telegram message row.
//
// Field meaning mirrors the messages table in schema.go. Times are
// always unix milliseconds. ChatJID is "tg:<chat_id>". Sender is the
// numeric Telegram user ID as a string ("bot" for our own outbound
// messages once those start being persisted).
type Message struct {
	ID                 int64
	ChatJID            string
	TGMessageID        int64
	Sender             string
	SenderName         string
	Content            string
	Timestamp          int64
	IsFromMe           bool
	IsBotMessage       bool
	ReplyToTGMessageID int64  // 0 if not a reply
	ReplyToContent     string // empty if not a reply
	ReplyToSenderName  string // empty if not a reply
}

// SaveMessage inserts m into the messages table. The (chat_jid, tg_message_id)
// uniqueness constraint makes this idempotent for re-delivered Telegram
// updates: a duplicate is silently dropped (no error, no row written) and
// the returned id is 0.
//
// Returns the inserted row's autoincrement id, or 0 if the message was
// a duplicate.
func (s *Store) SaveMessage(ctx context.Context, m Message) (int64, error) {
	if m.ChatJID == "" {
		return 0, errors.New("store: SaveMessage: empty ChatJID")
	}
	if m.TGMessageID == 0 {
		return 0, errors.New("store: SaveMessage: zero TGMessageID")
	}

	const stmt = `
		INSERT INTO messages (
		  chat_jid, tg_message_id, sender, sender_name, content, timestamp,
		  is_from_me, is_bot_message,
		  reply_to_tg_message_id, reply_to_content, reply_to_sender_name
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(chat_jid, tg_message_id) DO NOTHING`

	res, err := s.db.ExecContext(ctx, stmt,
		m.ChatJID,
		m.TGMessageID,
		m.Sender,
		nullableString(m.SenderName),
		m.Content,
		m.Timestamp,
		boolToInt(m.IsFromMe),
		boolToInt(m.IsBotMessage),
		nullableInt64(m.ReplyToTGMessageID),
		nullableString(m.ReplyToContent),
		nullableString(m.ReplyToSenderName),
	)
	if err != nil {
		return 0, fmt.Errorf("store: SaveMessage: insert: %w", err)
	}

	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: SaveMessage: LastInsertId: %w", err)
	}
	return id, nil
}

// CountMessages returns the total number of rows in messages.
// Used by tests and the (future) /stats control command.
func (s *Store) CountMessages(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: CountMessages: %w", err)
	}
	return n, nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// nullableString turns "" into a nil interface so SQLite stores NULL
// instead of an empty string. Keeps the schema sparse for absent fields.
func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// nullableInt64 turns 0 into NULL.
func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}
