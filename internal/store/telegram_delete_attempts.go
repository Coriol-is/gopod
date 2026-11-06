package store

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var ErrTelegramDeleteHeld = errors.New("store: message deletion outcome or tombstone requires reconciliation")

type TelegramDeleteAttempt struct {
	ID          string
	OwnerJSON   string
	RequestHash string
}

func (s *Store) applyTelegramDeleteSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_delete_attempts(
 attempt_id TEXT PRIMARY KEY,chat_id INTEGER NOT NULL,message_id INTEGER NOT NULL,
 source_thread INTEGER NOT NULL,owner_json TEXT NOT NULL,request_hash TEXT NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ack','refused')),
 CHECK(chat_id>0 AND message_id>0 AND source_thread>=0 AND length(request_hash)=64));
 CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_delete_held ON telegram_delete_attempts(chat_id,message_id) WHERE state IN ('pending','ack');
 CREATE TRIGGER IF NOT EXISTS telegram_delete_immutable BEFORE UPDATE OF attempt_id,chat_id,message_id,source_thread,owner_json,request_hash ON telegram_delete_attempts BEGIN SELECT RAISE(ABORT,'immutable delete attempt'); END;
 CREATE TRIGGER IF NOT EXISTS telegram_delete_preserve BEFORE DELETE ON telegram_delete_attempts WHEN OLD.state IN ('pending','ack') BEGIN SELECT RAISE(ABORT,'deletion evidence'); END;
 CREATE TRIGGER IF NOT EXISTS telegram_delete_settled BEFORE UPDATE OF state ON telegram_delete_attempts WHEN OLD.state!='pending' AND NEW.state!=OLD.state BEGIN SELECT RAISE(ABORT,'settled delete attempt'); END;`)
	return err
}
func deleteBarrierQ(ctx context.Context, q taskSQL, chat int64, msg int) error {
	var held bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_delete_attempts WHERE chat_id=? AND message_id=? AND state IN ('pending','ack'))`, chat, msg).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return ErrTelegramDeleteHeld
	}
	return nil
}
func (s *Store) BeginTelegramDelete(ctx context.Context, r TelegramEditRequest, expected TelegramEditOwner) (a TelegramDeleteAttempt, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := editPendingQ(ctx, q, r.ChatID, r.MessageID); e != nil {
			return e
		}
		if e := activeOutboxEditQ(ctx, q, r.ChatID, r.MessageID); e != nil {
			return e
		}
		o, covered, e := directEditOwnerQ(ctx, q, r.ChatID, r.MessageID)
		if e != nil {
			return e
		}
		if !covered {
			return ErrTelegramDeleteHeld
		}
		if _, e = hex.DecodeString(r.RequestHash); e != nil || len(r.RequestHash) != 64 || r.Method != "delete" {
			return ErrTelegramDeleteHeld
		}
		want, _ := json.Marshal(expected)
		raw, _ := json.Marshal(o)
		if string(want) != string(raw) {
			return ErrTelegramDeleteHeld
		}
		if r.SourceThread >= 0 && o.SourceThread != r.SourceThread {
			return ErrScopeMismatch
		}
		if o.SourceThread > 0 {
			if e = topicAdmissionQ(ctx, q, o.Scope, o.SourceThread); e != nil {
				return e
			}
		}
		var published bool
		e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM delivery_groups g JOIN outbox_parts p ON p.delivery_id=g.delivery_id WHERE g.chat_jid=? AND g.result_id IS NOT NULL AND ((p.kind='edit' AND p.target_message_id=?) OR (p.state='sent' AND p.remote_message_id=?)))`, o.Scope.JID, r.MessageID, r.MessageID).Scan(&published)
		if e != nil {
			return e
		}
		if published {
			return ErrTelegramDeleteHeld
		}
		id, e := newUUID()
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO telegram_delete_attempts(attempt_id,chat_id,message_id,source_thread,owner_json,request_hash) VALUES(?,?,?,?,?,?)`, id, r.ChatID, r.MessageID, o.SourceThread, string(raw), r.RequestHash)
		if e == nil {
			a = TelegramDeleteAttempt{ID: id, OwnerJSON: string(raw), RequestHash: r.RequestHash}
		}
		return e
	})
	return
}
func (s *Store) SettleTelegramDelete(ctx context.Context, a TelegramDeleteAttempt, ack bool) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var got TelegramDeleteAttempt
		var state string
		e := q.QueryRowContext(ctx, `SELECT attempt_id,owner_json,request_hash,state FROM telegram_delete_attempts WHERE attempt_id=?`, a.ID).Scan(&got.ID, &got.OwnerJSON, &got.RequestHash, &state)
		if e != nil {
			return e
		}
		if got != a {
			return ErrTelegramDeleteHeld
		}
		target := "refused"
		if ack {
			target = "ack"
		}
		if state == target {
			return nil
		}
		if state != "pending" {
			return ErrTelegramDeleteHeld
		}
		_, e = q.ExecContext(ctx, `UPDATE telegram_delete_attempts SET state=? WHERE attempt_id=?`, target, a.ID)
		if e != nil {
			return e
		}
		if ack {
			var o TelegramEditOwner
			if e = json.Unmarshal([]byte(a.OwnerJSON), &o); e != nil {
				return e
			}
			c := o.Control
			if c == nil {
				return nil
			}
			raw, _ := json.Marshal(c.Scope)
			_, e = q.ExecContext(ctx, `DELETE FROM telegram_control_messages WHERE chat_id=? AND message_id=? AND token=? AND revision=? AND turn_id=? AND scope_json=?`, c.ChatID, c.MessageID, c.Token, c.Revision, c.TurnID, string(raw))
		}
		return e
	})
	return err
}
