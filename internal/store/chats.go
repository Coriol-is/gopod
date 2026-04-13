package store

import (
	"context"
	"errors"
	"fmt"
)

// ChatRecord describes one row in the chats table — every chat gopod
// has ever heard from, registered or not.
type ChatRecord struct {
	JID             string // "tg:<chat_id>"
	Name            string // chat title for groups, user display name for private
	LastMessageTime int64  // unix ms
	IsGroup         bool
}

// UpsertChat inserts the chat or updates its name + last_message_time.
// Idempotent. Used by the telegram default handler on every inbound
// update so we always have an up-to-date view of which chats are alive.
func (s *Store) UpsertChat(ctx context.Context, c ChatRecord) error {
	if c.JID == "" {
		return errors.New("store: UpsertChat: empty JID")
	}

	const stmt = `
		INSERT INTO chats (jid, name, last_message_time, is_group)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
		  name              = excluded.name,
		  last_message_time = MAX(chats.last_message_time, excluded.last_message_time),
		  is_group          = excluded.is_group`

	if _, err := s.db.ExecContext(ctx, stmt,
		c.JID,
		nullableString(c.Name),
		c.LastMessageTime,
		boolToInt(c.IsGroup),
	); err != nil {
		return fmt.Errorf("store: UpsertChat: %w", err)
	}
	return nil
}

// CountChats returns the total number of rows in chats. Used by tests
// and (eventually) the /stats control command.
func (s *Store) CountChats(ctx context.Context) (int64, error) {
	var n int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM chats`).Scan(&n); err != nil {
		return 0, fmt.Errorf("store: CountChats: %w", err)
	}
	return n, nil
}
