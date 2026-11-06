package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

var ErrMessageOwnership = errors.New("store: telegram message ownership unresolved or conflicting")

// TelegramMessageOwner records host provenance, never message text or credentials.
// SourceThread can differ from TargetThread when a gone topic falls back to main.
type TelegramMessageOwner struct {
	AttemptID    string
	Scope        ScopeRef
	ChatID       int64
	MessageID    int
	SourceThread int
	TargetThread int
}

func (s *Store) applyTelegramOwnershipSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS telegram_message_ownership (
 attempt_id TEXT PRIMARY KEY, chat_id INTEGER NOT NULL, message_id INTEGER,
 scope_json TEXT NOT NULL, source_thread INTEGER NOT NULL, target_thread INTEGER NOT NULL,
 CHECK(chat_id>0 AND source_thread>=0 AND target_thread>=0),
 UNIQUE(chat_id,message_id))`)
	return err
}

// BeginTelegramMessageOwner commits provenance before a host-issued private-chat
// send. Only the source topic or its main-chat fallback is an allowed target.
// Unregistered diagnostics remain compatible; they return an empty attempt.
func (s *Store) BeginTelegramMessageOwner(ctx context.Context, chat int64, source, target int) (o TelegramMessageOwner, err error) {
	if chat <= 0 || source < 0 || target < 0 || (target != 0 && target != source) {
		return o, ErrInvalidConversation
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		jid := fmt.Sprintf("tg:%d", chat)
		_, e := readRegistered(ctx, q, "jid = ?", jid)
		if errors.Is(e, ErrChatNotRegistered) {
			return nil
		}
		if e != nil {
			return e
		}
		scope, e := snapshotScopeQ(ctx, q, "jid = ?", jid)
		if e != nil {
			return e
		}
		if source > 0 {
			if e = topicAdmissionQ(ctx, q, scope, source); e != nil {
				return e
			}
		}
		id, e := newUUID()
		if e != nil {
			return e
		}
		raw, e := json.Marshal(scope)
		if e != nil {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO telegram_message_ownership(attempt_id,chat_id,scope_json,source_thread,target_thread) VALUES(?,?,?,?,?)`, id, chat, string(raw), source, target)
		if e == nil {
			o = TelegramMessageOwner{AttemptID: id, Scope: scope, ChatID: chat, SourceThread: source, TargetThread: target}
		}
		return e
	})
	return
}

// AcknowledgeTelegramMessageOwner settles only the precommitted attempt. A
// registration change cannot relabel an old response with a new scope. Conflicts
// leave the attempt unresolved; callers must not replay the remote send.
func (s *Store) AcknowledgeTelegramMessageOwner(ctx context.Context, o TelegramMessageOwner, message int) error {
	if o.AttemptID == "" || message <= 0 {
		return ErrMessageOwnership
	}
	_, err := s.withTx(ctx, func(q taskSQL) error { return acknowledgeTelegramMessageOwnerQ(ctx, q, o, message) })
	return err
}

// ReconcileTelegramMessageOwner accepts only a host-retained positive receipt
// for the exact original attempt and still-current registration. It performs no
// remote request and cannot resolve an unknown outcome or a different epoch.
func (s *Store) ReconcileTelegramMessageOwner(ctx context.Context, o TelegramMessageOwner, message int) error {
	if o.AttemptID == "" || message <= 0 {
		return ErrMessageOwnership
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, o.Scope); err != nil {
			return err
		}
		return acknowledgeTelegramMessageOwnerQ(ctx, q, o, message)
	})
	return err
}
func acknowledgeTelegramMessageOwnerQ(ctx context.Context, q taskSQL, o TelegramMessageOwner, message int) error {
	var raw string
	var chat int64
	var src, dst int
	var existing sql.NullInt64
	if e := q.QueryRowContext(ctx, `SELECT scope_json,chat_id,source_thread,target_thread,message_id FROM telegram_message_ownership WHERE attempt_id=?`, o.AttemptID).Scan(&raw, &chat, &src, &dst, &existing); e != nil {
		return e
	}
	var scope ScopeRef
	if e := json.Unmarshal([]byte(raw), &scope); e != nil {
		return e
	}
	if scope != o.Scope || chat != o.ChatID || src != o.SourceThread || dst != o.TargetThread || (existing.Valid && existing.Int64 != int64(message)) {
		return ErrMessageOwnership
	}
	// Repeated acknowledgement is idempotent; another attempt can never replace it.
	_, e := q.ExecContext(ctx, `UPDATE telegram_message_ownership SET message_id=? WHERE attempt_id=?`, message, o.AttemptID)
	return e
}

// RefuseTelegramMessageOwner is only for definite Bot API refusal or proof of
// no dispatch. Unknown network results must retain the pending row.
func (s *Store) RefuseTelegramMessageOwner(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM telegram_message_ownership WHERE attempt_id=? AND message_id IS NULL`, id)
	return err
}
func (s *Store) TelegramMessageOwner(ctx context.Context, chat int64, message int) (o TelegramMessageOwner, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT attempt_id,scope_json,chat_id,message_id,source_thread,target_thread FROM telegram_message_ownership WHERE chat_id=? AND message_id=?`, chat, message).Scan(&o.AttemptID, &raw, &o.ChatID, &o.MessageID, &o.SourceThread, &o.TargetThread)
	if err == nil {
		err = json.Unmarshal([]byte(raw), &o.Scope)
	}
	if err == nil {
		err = s.ValidateScope(ctx, o.Scope)
	}
	return
}

// UnknownMainControlBlocked includes retired tombstones: old unowned fallback
// controls cannot silently gain main-chat authority after a restart.
func (s *Store) UnknownMainControlBlocked(ctx context.Context, chat int64) (bool, error) {
	var blocked bool
	err := s.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM topic_deletions WHERE chat_jid=? AND state!='refused') OR EXISTS(SELECT 1 FROM telegram_message_ownership WHERE chat_id=? AND message_id IS NULL)`, fmt.Sprintf("tg:%d", chat), chat).Scan(&blocked)
	return blocked, err
}
func (s *Store) TopicMessageOwnershipIdle(ctx context.Context, d TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if _, e := ownedTopicDeletionQ(ctx, q, d); e != nil {
			return e
		}
		var blocked bool
		// Raw source/recipient match remains conservative even across registration drift.
		e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_message_ownership WHERE chat_id=CAST(SUBSTR(?,4) AS INTEGER) AND source_thread=? AND message_id IS NULL)`, d.Scope.JID, d.ThreadID).Scan(&blocked)
		if e != nil {
			return e
		}
		if blocked {
			return ErrMessageOwnership
		}
		return nil
	})
	return err
}
