package store

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
)

func continuityKey(folder string, thread int, suffix string) string {
	return "continuity:" + folder + ":" + strconv.Itoa(thread) + ":" + suffix
}

// ConversationHandoff returns a bounded transcript only when this conversation
// last ran with another provider (or has no continuity marker yet). Every read
// is scoped by the registered chat folder and thread, including main (0).
func (s *Store) ConversationHandoff(ctx context.Context, folder string, thread int, provider string) (string, error) {
	if err := s.seedConversationHistory(ctx, folder, thread); err != nil {
		return "", err
	}
	last, err := s.GetState(ctx, continuityKey(folder, thread, "provider"))
	if err != nil {
		return "", err
	}
	if last == provider {
		return "", nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT role,substr(content,1,3000) FROM conversation_history
 WHERE chat_folder=? AND thread_id=? ORDER BY id DESC LIMIT 48`, folder, thread)
	if err != nil {
		return "", fmt.Errorf("reading conversation handoff: %w", err)
	}
	defer rows.Close()
	type exchange struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	}
	var recent []exchange
	size := 2
	for rows.Next() {
		var e exchange
		if err := rows.Scan(&e.Role, &e.Content); err != nil {
			return "", fmt.Errorf("scanning conversation handoff: %w", err)
		}
		encoded, err := json.Marshal(e)
		if err != nil {
			return "", err
		}
		if size+len(encoded)+1 > 48000 {
			break
		}
		size += len(encoded) + 1
		recent = append(recent, e)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("reading conversation handoff rows: %w", err)
	}
	if len(recent) == 0 {
		return "", nil
	}
	for i, j := 0, len(recent)-1; i < j; i, j = i+1, j-1 {
		recent[i], recent[j] = recent[j], recent[i]
	}
	b, err := json.Marshal(recent)
	return string(b), err
}

// CompleteConversation advances only after a successful agent run; a quota
// rejection must not consume the handoff before the next attempt.
func (s *Store) CompleteConversation(ctx context.Context, folder string, thread int, provider, reply string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = completeConversationQ(ctx, tx, folder, thread, provider, reply); err != nil {
		return err
	}
	return tx.Commit()
}

// completeConversationQ is the transaction-aware body of CompleteConversation.
// It is shared with FreezeTurnResult, which must append continuity inside the
// result transaction and cannot call the public API (single connection).
func completeConversationQ(ctx context.Context, q taskSQL, folder string, thread int, provider, reply string) error {
	if _, err := q.ExecContext(ctx, `INSERT INTO conversation_history(chat_folder,thread_id,role,content) VALUES(?,?,'assistant',?)`, folder, thread, reply); err != nil {
		return err
	}
	_, err := q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, continuityKey(folder, thread, "provider"), provider)
	return err
}

func (s *Store) RecordConversationRequest(ctx context.Context, folder string, thread int, prompt string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO conversation_history(chat_folder,thread_id,role,content) VALUES(?,?,'user',?)`, folder, thread, prompt)
	if err != nil {
		return fmt.Errorf("recording conversation request: %w", err)
	}
	return nil
}

// Seed the pre-continuity installation once, in a transaction. New runs record
// their transcript before releasing the agent slot, independent of Telegram delivery.
func (s *Store) seedConversationHistory(ctx context.Context, folder string, thread int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	key := continuityKey(folder, thread, "seeded")
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM router_state WHERE key=?`, key).Scan(&exists); err != nil {
		return err
	}
	if exists > 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT text,reply_msg_id,chat_id FROM
 (SELECT id,text,reply_msg_id,chat_id FROM turns WHERE chat_folder=? AND thread_id=? AND source='telegram'
 AND status IN ('done','stopped','failed') ORDER BY id DESC LIMIT 24) ORDER BY id`, folder, thread)
	if err != nil {
		return err
	}
	type oldTurn struct {
		text        string
		reply, chat int64
	}
	var old []oldTurn
	for rows.Next() {
		var o oldTurn
		if err = rows.Scan(&o.text, &o.reply, &o.chat); err != nil {
			rows.Close()
			return err
		}
		old = append(old, o)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, o := range old {
		if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_history(chat_folder,thread_id,role,content) VALUES(?,?,'user',?)`, folder, thread, o.text); err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO conversation_history(chat_folder,thread_id,role,content)
   SELECT ?,?,'assistant',content FROM messages WHERE chat_jid=? AND tg_message_id=? AND is_from_me=1`, folder, thread, "tg:"+strconv.FormatInt(o.chat, 10), o.reply); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,'1')`, key); err != nil {
		return err
	}
	return tx.Commit()
}

// ClearConversationContinuity makes /clear a boundary across all providers in
// this one thread. The audit transcript remains stored but is never replayed.
func (s *Store) ClearConversationContinuity(ctx context.Context, folder string, thread int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning conversation clear: %w", err)
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `DELETE FROM conversation_history WHERE chat_folder=? AND thread_id=?`, folder, thread); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,'1') ON CONFLICT(key) DO NOTHING`, continuityKey(folder, thread, "seeded")); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM sessions WHERE chat_folder=? AND thread_id=?`, folder, thread); err != nil {
		return err
	}
	// Prevent a previously unused provider from re-adopting an old main session.
	for _, p := range []string{"claude", "codex"} {
		if thread != 0 {
			break
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,'1') ON CONFLICT(key) DO NOTHING`, "session_adopted:"+folder+":"+p); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// PersistProviderChoice atomically resets the harness-specific model override.
func (s *Store) PersistProviderChoice(ctx context.Context, folder, provider string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for key, value := range map[string]string{"provider:" + folder: provider, "model:" + folder: ""} {
		if _, err = tx.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value); err != nil {
			return err
		}
	}
	return tx.Commit()
}
