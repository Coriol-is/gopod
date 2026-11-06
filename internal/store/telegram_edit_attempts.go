package store

import (
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrTelegramEditHeld = errors.New("store: direct edit ownership or outcome requires reconciliation")

type TelegramEditOwner struct {
	ChatID       int64
	MessageID    int
	Scope        ScopeRef
	SourceThread int
	Evidence     string
	Control      *ControlMessage
}
type TelegramEditRequest struct {
	ChatID       int64
	MessageID    int
	SourceThread int // -1 means no supplied origin; never grants provenance
	Method       string
	RequestHash  string
	FinalTurn    int64 // only the exact durable reply callback may edit final markup
}
type TelegramEditAttempt struct {
	ID          string
	OwnerJSON   string
	Method      string
	RequestHash string
	Final       bool
}

func (s *Store) applyTelegramEditSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_edit_attempts(
 attempt_id TEXT PRIMARY KEY,chat_id INTEGER NOT NULL,message_id INTEGER NOT NULL,
 source_thread INTEGER NOT NULL,owner_json TEXT NOT NULL,method TEXT NOT NULL CHECK(method IN ('text','markup')),
 request_hash TEXT NOT NULL,final INTEGER NOT NULL,state TEXT NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','ack','refused')),
 CHECK(chat_id>0 AND message_id>0 AND source_thread>=0 AND length(request_hash)=64));
 CREATE UNIQUE INDEX IF NOT EXISTS idx_telegram_edit_pending ON telegram_edit_attempts(chat_id,message_id) WHERE state='pending';
 CREATE TRIGGER IF NOT EXISTS telegram_edit_immutable BEFORE UPDATE OF attempt_id,chat_id,message_id,source_thread,owner_json,method,request_hash,final ON telegram_edit_attempts BEGIN SELECT RAISE(ABORT,'immutable edit attempt'); END;
 CREATE TRIGGER IF NOT EXISTS telegram_edit_pending_delete BEFORE DELETE ON telegram_edit_attempts WHEN OLD.state='pending' BEGIN SELECT RAISE(ABORT,'unresolved edit attempt'); END;
 CREATE TRIGGER IF NOT EXISTS telegram_edit_settled BEFORE UPDATE OF state ON telegram_edit_attempts WHEN OLD.state!='pending' AND NEW.state!=OLD.state BEGIN SELECT RAISE(ABORT,'settled edit attempt'); END;`)
	return err
}
func editPendingQ(ctx context.Context, q taskSQL, chat int64, msg int) error {
	var repair, direct bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_control_repair_attempts WHERE chat_id=? AND message_id=? AND state='pending'), EXISTS(SELECT 1 FROM telegram_edit_attempts WHERE chat_id=? AND message_id=? AND state='pending')`, chat, msg, chat, msg).Scan(&repair, &direct)
	if err != nil {
		return err
	}
	if repair {
		return ErrControlRepairPending
	}
	if direct {
		return ErrTelegramEditHeld
	}
	return nil
}
func activeOutboxEditQ(ctx context.Context, q taskSQL, chat int64, msg int) error {
	var held bool
	err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE g.chat_jid=? AND p.kind='edit' AND p.target_message_id=? AND p.state IN ('pending','sending','uncertain'))`, fmt.Sprintf("tg:%d", chat), msg).Scan(&held)
	if err != nil {
		return err
	}
	if held {
		return ErrTelegramEditHeld
	}
	return nil
}

// ResolveDirectEditOwner reads durable provenance only. The process cache is not
// evidence. A registered-private historical target with no evidence is held.
func (s *Store) ResolveDirectEditOwner(ctx context.Context, chat int64, msg int) (owner TelegramEditOwner, covered bool, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error { var e error; owner, covered, e = directEditOwnerQ(ctx, q, chat, msg); return e })
	return
}
func directEditOwnerQ(ctx context.Context, q taskSQL, chat int64, msg int) (o TelegramEditOwner, covered bool, err error) {
	if chat <= 0 {
		return o, false, nil
	}
	jid := fmt.Sprintf("tg:%d", chat)
	_, e := readRegistered(ctx, q, "jid = ?", jid)
	if errors.Is(e, ErrChatNotRegistered) {
		return o, false, nil
	}
	if e != nil {
		return o, true, e
	}
	scope, e := snapshotScopeQ(ctx, q, "jid = ?", jid)
	if errors.Is(e, ErrChatNotRegistered) {
		return o, false, nil
	}
	if e != nil {
		return o, true, e
	}
	o = TelegramEditOwner{ChatID: chat, MessageID: msg, Scope: scope}
	var c ControlMessage
	var raw string
	e = q.QueryRowContext(ctx, `SELECT chat_id,message_id,turn_id,token,scope_json,thread_id,text,html,terminal,revision,acknowledged,retry_at FROM telegram_control_messages WHERE chat_id=? AND message_id=?`, chat, msg).Scan(&c.ChatID, &c.MessageID, &c.TurnID, &c.Token, &raw, &c.ThreadID, &c.Text, &c.HTML, &c.Terminal, &c.Revision, &c.Acknowledged, &c.RetryAt)
	if e == nil {
		if e = json.Unmarshal([]byte(raw), &c.Scope); e != nil {
			return o, true, e
		}
		if c.Scope != scope {
			return o, true, ErrStaleScope
		}
		o.SourceThread = c.ThreadID
		o.Evidence = "control"
		o.Control = &c
		return o, true, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return o, true, e
	}
	var id string
	var thread int
	e = q.QueryRowContext(ctx, `SELECT attempt_id,scope_json,source_thread FROM telegram_message_ownership WHERE chat_id=? AND message_id=?`, chat, msg).Scan(&id, &raw, &thread)
	if e == nil {
		var saved ScopeRef
		if e = json.Unmarshal([]byte(raw), &saved); e != nil {
			return o, true, e
		}
		if saved != scope {
			return o, true, ErrStaleScope
		}
		o.SourceThread = thread
		o.Evidence = "creation:" + id
		return o, true, nil
	}
	if !errors.Is(e, sql.ErrNoRows) {
		return o, true, e
	}
	e = q.QueryRowContext(ctx, `SELECT p.part_id,g.thread_id FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE g.chat_jid=? AND g.namespace_id=? AND g.namespace_epoch=? AND g.registration_revision=? AND g.visibility='conversation' AND p.state='sent' AND p.remote_message_id=? ORDER BY g.conversation_seq DESC,p.ordinal DESC LIMIT 1`, jid, scope.NamespaceID, scope.NamespaceEpoch, scope.RegistrationRevision, msg).Scan(&id, &thread)
	if errors.Is(e, sql.ErrNoRows) {
		return o, true, ErrTelegramEditHeld
	}
	if e != nil {
		return o, true, e
	}
	o.SourceThread = thread
	o.Evidence = "outbox:" + id
	return o, true, nil
}
func (s *Store) BeginTelegramEdit(ctx context.Context, r TelegramEditRequest, expected TelegramEditOwner, final bool) (a TelegramEditAttempt, err error) {
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
			return ErrTelegramEditHeld
		}
		if _, e = hex.DecodeString(r.RequestHash); e != nil || len(r.RequestHash) != 64 || r.Method != "text" && r.Method != "markup" {
			return ErrTelegramEditHeld
		}
		want, _ := json.Marshal(expected)
		raw, _ := json.Marshal(o)
		if string(want) != string(raw) {
			return ErrTelegramEditHeld
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
			if r.Method != "markup" || r.FinalTurn <= 0 {
				return ErrTelegramEditHeld
			}
			var action AcknowledgedReplyAction
			if e = replyActionQ(ctx, q, r.FinalTurn, &action); e != nil {
				return e
			}
			if !action.Eligible || !action.Newest || action.MessageID != int64(r.MessageID) || action.Conversation.Scope != o.Scope || action.Conversation.ThreadID != o.SourceThread {
				return ErrTelegramEditHeld
			}
		}
		if final && (r.Method != "text" || o.Control == nil || !o.Control.Terminal) {
			return ErrTelegramEditHeld
		}
		id, e := newUUID()
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO telegram_edit_attempts(attempt_id,chat_id,message_id,source_thread,owner_json,method,request_hash,final) VALUES(?,?,?,?,?,?,?,?)`, id, r.ChatID, r.MessageID, o.SourceThread, string(raw), r.Method, r.RequestHash, final)
		if e == nil {
			a = TelegramEditAttempt{ID: id, OwnerJSON: string(raw), Method: r.Method, RequestHash: r.RequestHash, Final: final}
		}
		return e
	})
	return
}
func (s *Store) SettleTelegramEdit(ctx context.Context, a TelegramEditAttempt, ack bool) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var got TelegramEditAttempt
		var state string
		e := q.QueryRowContext(ctx, `SELECT attempt_id,owner_json,method,request_hash,final,state FROM telegram_edit_attempts WHERE attempt_id=?`, a.ID).Scan(&got.ID, &got.OwnerJSON, &got.Method, &got.RequestHash, &got.Final, &state)
		if e != nil {
			return e
		}
		if got != a {
			return ErrTelegramEditHeld
		}
		target := "refused"
		if ack {
			target = "ack"
		}
		if state == target {
			return nil
		}
		if state != "pending" {
			return ErrTelegramEditHeld
		}
		_, e = q.ExecContext(ctx, `UPDATE telegram_edit_attempts SET state=? WHERE attempt_id=?`, target, a.ID)
		if e != nil {
			return e
		}
		if ack && a.Final {
			var o TelegramEditOwner
			if e = json.Unmarshal([]byte(a.OwnerJSON), &o); e != nil {
				return e
			}
			c := o.Control
			if c == nil {
				return ErrTelegramEditHeld
			}
			raw, _ := json.Marshal(c.Scope)
			_, e = q.ExecContext(ctx, `UPDATE telegram_control_messages SET acknowledged=revision WHERE chat_id=? AND message_id=? AND token=? AND revision=? AND turn_id=? AND scope_json=? AND terminal=1`, c.ChatID, c.MessageID, c.Token, c.Revision, c.TurnID, string(raw))
		}
		return e
	})
	return err
}
