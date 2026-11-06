package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
)

var ErrControlRepairPending = errors.New("store: control repair outcome requires reconciliation")

// ControlRepairAttempt is immutable host evidence, never authority to replay HTTP.
type ControlRepairAttempt struct {
	Final        bool
	ID           string
	ChatID       int64
	MessageID    int
	TurnID       int64
	Token        string
	Revision     int64
	Scope        ScopeRef
	SourceThread int
	RequestHash  string
}

func (s *Store) applyControlRepairSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_control_repair_attempts (
 attempt_id TEXT PRIMARY KEY, chat_id INTEGER NOT NULL, message_id INTEGER NOT NULL,
 turn_id INTEGER NOT NULL, token TEXT NOT NULL, revision INTEGER NOT NULL,
 scope_json TEXT NOT NULL, source_thread INTEGER NOT NULL, request_hash TEXT NOT NULL, final INTEGER NOT NULL,
 state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ack','refused')),
 CHECK(message_id>0 AND revision>0 AND source_thread>=0 AND length(request_hash)=64));
 CREATE UNIQUE INDEX IF NOT EXISTS idx_control_repair_pending ON telegram_control_repair_attempts(chat_id,message_id) WHERE state='pending';
 CREATE TRIGGER IF NOT EXISTS control_repair_immutable BEFORE UPDATE OF attempt_id,chat_id,message_id,turn_id,token,revision,scope_json,source_thread,request_hash,final ON telegram_control_repair_attempts BEGIN SELECT RAISE(ABORT,'immutable repair attempt'); END;
 CREATE TRIGGER IF NOT EXISTS control_repair_pending_delete BEFORE DELETE ON telegram_control_repair_attempts WHEN OLD.state='pending' BEGIN SELECT RAISE(ABORT,'unresolved repair attempt'); END;
 CREATE TRIGGER IF NOT EXISTS control_repair_settled BEFORE UPDATE OF state ON telegram_control_repair_attempts WHEN OLD.state!='pending' AND NEW.state!=OLD.state BEGIN SELECT RAISE(ABORT,'settled repair attempt'); END;`)
	return err
}
func (s *Store) BeginControlRepair(ctx context.Context, c ControlMessage, hash string, final bool) (a ControlRepairAttempt, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if _, e := hex.DecodeString(hash); e != nil || len(hash) != 64 {
			return ErrControlRepairPending
		}
		if e := validateScopeQ(ctx, q, c.Scope); e != nil {
			return e
		}
		if c.ThreadID > 0 {
			if e := topicAdmissionQ(ctx, q, c.Scope, c.ThreadID); e != nil {
				return e
			}
		}
		var token, raw string
		var revision, turn int64
		var thread int
		var terminal bool
		var acknowledged int64
		e := q.QueryRowContext(ctx, `SELECT token,revision,turn_id,scope_json,thread_id,terminal,acknowledged FROM telegram_control_messages WHERE chat_id=? AND message_id=?`, c.ChatID, c.MessageID).Scan(&token, &revision, &turn, &raw, &thread, &terminal, &acknowledged)
		if e != nil {
			return e
		}
		var scope ScopeRef
		if e = json.Unmarshal([]byte(raw), &scope); e != nil {
			return e
		}
		if terminal && acknowledged >= revision {
			return ErrControlRepairPending
		}
		if token != c.Token || revision != c.Revision || turn != c.TurnID || scope != c.Scope || thread != c.ThreadID {
			return ErrControlRepairPending
		}
		held, e := controlFinalOwnershipQ(ctx, q, c)
		if e != nil {
			return e
		}
		if held {
			return ErrControlRepairPending
		}
		var pending bool
		if e = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_control_repair_attempts WHERE chat_id=? AND message_id=? AND state='pending')`, c.ChatID, c.MessageID).Scan(&pending); e != nil {
			return e
		}
		if pending {
			return ErrControlRepairPending
		}
		id, e := newUUID()
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO telegram_control_repair_attempts(attempt_id,chat_id,message_id,turn_id,token,revision,scope_json,source_thread,request_hash,final) VALUES(?,?,?,?,?,?,?,?,?,?)`, id, c.ChatID, c.MessageID, c.TurnID, c.Token, c.Revision, raw, c.ThreadID, hash, final)
		if e == nil {
			a = ControlRepairAttempt{Final: final, ID: id, ChatID: c.ChatID, MessageID: c.MessageID, TurnID: c.TurnID, Token: c.Token, Revision: c.Revision, Scope: c.Scope, SourceThread: c.ThreadID, RequestHash: hash}
		}
		return e
	})
	return
}

// SettleControlRepair uses exact original receipt identity; it never updates a
// current control revision and remains valid evidence after scope rotation.
func (s *Store) SettleControlRepair(ctx context.Context, a ControlRepairAttempt, ack bool) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var got ControlRepairAttempt
		var raw, state string
		e := q.QueryRowContext(ctx, `SELECT attempt_id,chat_id,message_id,turn_id,token,revision,scope_json,source_thread,request_hash,final,state FROM telegram_control_repair_attempts WHERE attempt_id=?`, a.ID).Scan(&got.ID, &got.ChatID, &got.MessageID, &got.TurnID, &got.Token, &got.Revision, &raw, &got.SourceThread, &got.RequestHash, &got.Final, &state)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrControlRepairPending
		}
		if e != nil {
			return e
		}
		if e = json.Unmarshal([]byte(raw), &got.Scope); e != nil {
			return e
		}
		if got != a {
			return ErrControlRepairPending
		}
		target := "refused"
		if ack {
			target = "ack"
		}
		if state == target {
			return nil
		}
		if state != "pending" {
			return ErrControlRepairPending
		}
		_, e = q.ExecContext(ctx, `UPDATE telegram_control_repair_attempts SET state=? WHERE attempt_id=? AND state='pending'`, target, a.ID)
		if e != nil {
			return e
		}
		if ack && a.Final {
			_, e = q.ExecContext(ctx, `UPDATE telegram_control_messages SET terminal=1,acknowledged=revision,retry_at=0 WHERE chat_id=? AND message_id=? AND token=? AND revision=? AND turn_id=? AND scope_json=? AND thread_id=?`, a.ChatID, a.MessageID, a.Token, a.Revision, a.TurnID, raw, a.SourceThread)
		}
		return e
	})
	return err
}
func (s *Store) ControlRepairBarrier(ctx context.Context, chat int64, msg int) error {
	var held bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_control_repair_attempts WHERE chat_id=? AND message_id=? AND state='pending')`, chat, msg).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return ErrControlRepairPending
	}
	return nil
}
