package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// ReplySurface is durable metadata for a final text ACK. It contains no reply
// content and is not itself authorization to edit a message.
type ReplySurface struct {
	Sequence     int64
	Conversation ConversationScope
	MessageID    int64
	TurnID       int64
	Source       string
}

func reserveReplySurfaceQ(ctx context.Context, q taskSQL, conv ConversationScope, effect string, turn int64, source string) error {
	if err := validateConversationQ(ctx, q, conv); err != nil {
		return err
	}
	raw, err := json.Marshal(conv.Scope)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO telegram_reply_surfaces(effect_id,scope_json,namespace_id,namespace_epoch,registration_revision,thread_id,visibility,turn_id,source) VALUES(?,?,?,?,?,?,?,?,?) ON CONFLICT(effect_id) DO NOTHING`, effect, string(raw), conv.Scope.NamespaceID, conv.Scope.NamespaceEpoch, conv.Scope.RegistrationRevision, conv.ThreadID, conv.Visibility, turn, source)
	if err != nil {
		return err
	}
	var gotScope, gotVisibility, gotSource string
	var gotThread int
	var gotTurn int64
	if err = q.QueryRowContext(ctx, `SELECT scope_json,thread_id,visibility,turn_id,source FROM telegram_reply_surfaces WHERE effect_id=?`, effect).Scan(&gotScope, &gotThread, &gotVisibility, &gotTurn, &gotSource); err != nil {
		return err
	}
	if gotScope != string(raw) || gotThread != conv.ThreadID || gotVisibility != string(conv.Visibility) || gotTurn != turn || gotSource != source {
		return ErrTelegramEditHeld
	}
	return nil
}

// A receipt never creates chronology. Historical sends without a reservation
// are left unindexed, and cannot become newer because their DB repair ran late.
func recordReplySurfaceQ(ctx context.Context, q taskSQL, conv ConversationScope, effect string, message, turn int64, source string) error {
	var raw, kind string
	var thread int
	var id, msg int64
	err := q.QueryRowContext(ctx, `SELECT scope_json,thread_id,turn_id,source,message_id FROM telegram_reply_surfaces WHERE effect_id=?`, effect).Scan(&raw, &thread, &id, &kind, &msg)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	want, _ := json.Marshal(conv.Scope)
	if raw != string(want) || thread != conv.ThreadID || id != turn || kind != source || msg != 0 && msg != message {
		return ErrTelegramEditHeld
	}
	_, err = q.ExecContext(ctx, `UPDATE telegram_reply_surfaces SET message_id=? WHERE effect_id=?`, message, effect)
	return err
}

// ReserveLegacyReplySurface runs after final ownership but before any final HTTP.
// It records intended ordering only, never message delivery or callback authority.
func (s *Store) ReserveLegacyReplySurface(ctx context.Context, conv ConversationScope, turnID int64) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var folder, source string
		var thread int
		if e := q.QueryRowContext(ctx, `SELECT chat_folder,thread_id,source FROM turns WHERE id=?`, turnID).Scan(&folder, &thread, &source); e != nil {
			return e
		}
		if folder != conv.Scope.Folder || thread != conv.ThreadID || source == "task" {
			return ErrScopeMismatch
		}
		return reserveReplySurfaceQ(ctx, q, conv, fmt.Sprintf("legacy:%d", turnID), turnID, "legacy")
	})
	return err
}

func latestReplySurfaceQ(ctx context.Context, q taskSQL, conv ConversationScope) (out ReplySurface, found bool, err error) {
	var raw string
	err = q.QueryRowContext(ctx, `SELECT sequence,scope_json,message_id,turn_id,source FROM telegram_reply_surfaces WHERE namespace_id=? AND namespace_epoch=? AND registration_revision=? AND thread_id=? AND visibility=? AND message_id>0 ORDER BY sequence DESC LIMIT 1`, conv.Scope.NamespaceID, conv.Scope.NamespaceEpoch, conv.Scope.RegistrationRevision, conv.ThreadID, conv.Visibility).Scan(&out.Sequence, &raw, &out.MessageID, &out.TurnID, &out.Source)
	if errors.Is(err, sql.ErrNoRows) {
		return out, false, nil
	}
	if err != nil {
		return out, false, err
	}
	var scope ScopeRef
	if err = json.Unmarshal([]byte(raw), &scope); err != nil {
		return out, false, err
	}
	if scope != conv.Scope {
		return out, false, ErrScopeMismatch
	}
	out.Conversation = conv
	return out, true, nil
}

// LatestReplySurface reads only the current registration's final observations.
// Missing means historical ordering is unknown, never that an old row is newest.
func (s *Store) LatestReplySurface(ctx context.Context, conv ConversationScope) (out ReplySurface, found bool, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := validateConversationQ(ctx, q, conv); e != nil {
			return e
		}
		var e error
		out, found, e = latestReplySurfaceQ(ctx, q, conv)
		return e
	})
	return
}

// RecordLegacyReplySurface records a legacy final only after durable provenance
// and any terminal control text ACK agree. A pending/uncertain edit can never
// advance the shared row order. Older unproven messages remain legacy-only.
func (s *Store) RecordLegacyReplySurface(ctx context.Context, turnID int64, message int) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var chat int64
		var folder, source string
		var thread, reply int
		if e := q.QueryRowContext(ctx, `SELECT chat_id,chat_folder,thread_id,source,reply_msg_id FROM turns WHERE id=?`, turnID).Scan(&chat, &folder, &thread, &source, &reply); e != nil {
			return e
		}
		if chat <= 0 || source == "task" || reply != message || message <= 0 {
			return ErrTelegramEditHeld
		}
		var frozen bool
		if e := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turn_result_members WHERE turn_id=?)`, turnID).Scan(&frozen); e != nil {
			return e
		}
		if frozen {
			return ErrTelegramEditHeld
		}
		o, covered, e := directEditOwnerQ(ctx, q, chat, message)
		if e != nil {
			return e
		}
		if !covered || o.Scope.Folder != folder || o.SourceThread != thread {
			return ErrTelegramEditHeld
		}
		if e = editPendingQ(ctx, q, chat, message); e != nil {
			return e
		}
		if e = activeOutboxEditQ(ctx, q, chat, message); e != nil {
			return e
		}
		if c := o.Control; c != nil && (!c.Terminal || c.Acknowledged < c.Revision || c.TurnID != turnID) {
			return ErrTelegramEditHeld
		}
		return recordReplySurfaceQ(ctx, q, ConversationScope{Scope: o.Scope, ThreadID: thread, Visibility: VisibilityConversation}, fmt.Sprintf("legacy:%d", turnID), int64(message), turnID, "legacy")
	})
	return err
}

// ReplySurfaceSequence returns observed order for an exact message. Missing
// evidence cannot establish that a process-local legacy row is older.
func (s *Store) ReplySurfaceSequence(ctx context.Context, conv ConversationScope, message int64) (seq int64, found bool, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := validateConversationQ(ctx, q, conv); e != nil {
			return e
		}
		e := q.QueryRowContext(ctx, `SELECT sequence FROM telegram_reply_surfaces WHERE namespace_id=? AND namespace_epoch=? AND registration_revision=? AND thread_id=? AND visibility=? AND message_id=? ORDER BY sequence DESC LIMIT 1`, conv.Scope.NamespaceID, conv.Scope.NamespaceEpoch, conv.Scope.RegistrationRevision, conv.ThreadID, conv.Visibility, message).Scan(&seq)
		if errors.Is(e, sql.ErrNoRows) {
			return nil
		}
		found = e == nil
		return e
	})
	return
}
