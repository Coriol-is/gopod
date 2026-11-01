package store

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ControlMessage binds a persistent Telegram control surface to one execution.
// Scope is immutable: retired/re-registered chats must not receive old repairs.
type ControlMessage struct {
	ChatID       int64
	MessageID    int
	TurnID       int64
	Token        string
	Scope        ScopeRef
	ThreadID     int
	Text         string
	HTML         bool
	Terminal     bool
	Revision     int64
	Acknowledged int64
	RetryAt      int64
}

func (s *Store) applyControlMessageSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_control_messages (
 chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL, turn_id INTEGER NOT NULL REFERENCES turns(id),
 token TEXT NOT NULL, scope_json TEXT NOT NULL, thread_id INTEGER NOT NULL,
 text TEXT NOT NULL DEFAULT '', html INTEGER NOT NULL DEFAULT 0, terminal INTEGER NOT NULL DEFAULT 0,
 revision INTEGER NOT NULL DEFAULT 1, acknowledged INTEGER NOT NULL DEFAULT 0,
 retry_at INTEGER NOT NULL DEFAULT 0, PRIMARY KEY(chat_id,message_id))`)
	return err
}

func (s *Store) BindControlMessage(ctx context.Context, c ControlMessage) error {
	if c.ChatID == 0 || c.MessageID <= 0 || c.TurnID <= 0 || c.Token == "" {
		return fmt.Errorf("invalid control message")
	}
	if err := s.ValidateScope(ctx, c.Scope); err != nil {
		return err
	}
	raw, err := json.Marshal(c.Scope)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `INSERT INTO telegram_control_messages(chat_id,message_id,turn_id,token,scope_json,thread_id) VALUES(?,?,?,?,?,?) ON CONFLICT(chat_id,message_id) DO NOTHING`, c.ChatID, c.MessageID, c.TurnID, c.Token, string(raw), c.ThreadID)
	return err
}

func (s *Store) CheckpointControlMessage(ctx context.Context, chat int64, msg int, text string, html, terminal bool) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_control_messages SET text=?,html=?,terminal=?,revision=revision+1,retry_at=0 WHERE chat_id=? AND message_id=? AND terminal=0`, text, html, terminal, chat, msg)
	return err
}

func (s *Store) ControlMessages(ctx context.Context) ([]ControlMessage, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT chat_id,message_id,turn_id,token,scope_json,thread_id,text,html,terminal,revision,acknowledged,retry_at FROM telegram_control_messages WHERE (terminal=0 OR acknowledged<revision) AND retry_at<=? ORDER BY retry_at,chat_id,message_id LIMIT 100`, time.Now().UnixMilli())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ControlMessage
	for rows.Next() {
		var c ControlMessage
		var raw string
		if err = rows.Scan(&c.ChatID, &c.MessageID, &c.TurnID, &c.Token, &raw, &c.ThreadID, &c.Text, &c.HTML, &c.Terminal, &c.Revision, &c.Acknowledged, &c.RetryAt); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &c.Scope); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// FinishControlRepair CASes the revision; failed edits retain durable intent.
func (s *Store) FinishControlRepair(ctx context.Context, c ControlMessage, done bool, retryAt int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_control_messages SET acknowledged=CASE WHEN ? THEN revision ELSE acknowledged END, terminal=CASE WHEN ? THEN 1 ELSE terminal END,retry_at=? WHERE chat_id=? AND message_id=? AND revision=?`, done, done, retryAt, c.ChatID, c.MessageID, c.Revision)
	return err
}

func (s *Store) GetControlMessage(ctx context.Context, chat int64, msg int) (ControlMessage, error) {
	var c ControlMessage
	var raw string
	err := s.db.QueryRowContext(ctx, `SELECT chat_id,message_id,turn_id,token,scope_json,thread_id,text,html,terminal,revision,acknowledged,retry_at FROM telegram_control_messages WHERE chat_id=? AND message_id=?`, chat, msg).Scan(&c.ChatID, &c.MessageID, &c.TurnID, &c.Token, &raw, &c.ThreadID, &c.Text, &c.HTML, &c.Terminal, &c.Revision, &c.Acknowledged, &c.RetryAt)
	if err != nil {
		return c, err
	}
	err = json.Unmarshal([]byte(raw), &c.Scope)
	return c, err
}
func (s *Store) AcknowledgeControlEdit(ctx context.Context, chat int64, msg int) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_control_messages SET acknowledged=revision WHERE chat_id=? AND message_id=? AND terminal=1`, chat, msg)
	return err
}

// RequeueControlRepair is used after an obsolete button press. It does not send
// anything and cannot change ownership or the execution state.
func (s *Store) RequeueControlRepair(ctx context.Context, chat int64, msg int, token string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE telegram_control_messages SET acknowledged=0,retry_at=0 WHERE chat_id=? AND message_id=? AND token=?`, chat, msg, token)
	return err
}
