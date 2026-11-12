package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// ObserveDeletedTelegramTopic accepts an exact host-observed TOPIC_DELETED
// refusal for a precommitted send into that same private topic. It atomically
// settles only that send and records deletion evidence. It never issues remote
// deletion or authorizes cleanup before native/queue/transport quiescence.
func (s *Store) ObserveDeletedTelegramTopic(ctx context.Context, owner TelegramMessageOwner) (d TopicDeletion, err error) {
	if owner.AttemptID == "" || owner.SourceThread <= 0 || owner.TargetThread != owner.SourceThread || owner.ChatID <= 0 || owner.Scope.JID != fmt.Sprintf("tg:%d", owner.ChatID) {
		return d, ErrInvalidConversation
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if e := validatePrivateTopicQ(ctx, q, owner.Scope, owner.SourceThread); e != nil {
			return e
		}
		old, oldErr := readTopicDeletionQ(ctx, q, owner.Scope, owner.SourceThread)
		if oldErr != nil && !errors.Is(oldErr, sql.ErrNoRows) {
			return oldErr
		}
		if oldErr == nil && old.Scope != owner.Scope {
			return ErrStaleScope
		}
		if oldErr == nil {
			var observed string
			if e := q.QueryRowContext(ctx, `SELECT observation_attempt_id FROM topic_deletions WHERE intent_id=?`, old.IntentID).Scan(&observed); e != nil {
				return e
			}
			if observed == owner.AttemptID && (old.State == "deleted" || old.State == "retired") {
				d = old
				return nil
			}
		}
		var raw string
		var chat int64
		var source, target int
		var message sql.NullInt64
		if e := q.QueryRowContext(ctx, `SELECT scope_json,chat_id,source_thread,target_thread,message_id FROM telegram_message_ownership WHERE attempt_id=?`, owner.AttemptID).Scan(&raw, &chat, &source, &target, &message); e != nil {
			return e
		}
		var scope ScopeRef
		if e := json.Unmarshal([]byte(raw), &scope); e != nil {
			return e
		}
		if scope != owner.Scope || chat != owner.ChatID || source != owner.SourceThread || target != owner.TargetThread || message.Valid {
			return ErrMessageOwnership
		}
		d = old
		if errors.Is(oldErr, sql.ErrNoRows) {
			id, e := newUUID()
			if e != nil {
				return e
			}
			d = TopicDeletion{Scope: owner.Scope, ThreadID: source, IntentID: id, State: "deleted", ObservationAttemptID: owner.AttemptID}
			if _, e = q.ExecContext(ctx, `INSERT INTO topic_deletions(namespace_id,namespace_epoch,thread_id,intent_id,registration_revision,chat_jid,folder,state,updated_at,dispatch_started,observation_attempt_id) VALUES(?,?,?,?,?,?,?,'deleted',?,1,?) ON CONFLICT(namespace_id,namespace_epoch,thread_id) DO UPDATE SET intent_id=excluded.intent_id,state='deleted',updated_at=excluded.updated_at,dispatch_started=1,observation_attempt_id=excluded.observation_attempt_id`, owner.Scope.NamespaceID, owner.Scope.NamespaceEpoch, source, id, owner.Scope.RegistrationRevision, owner.Scope.JID, owner.Scope.Folder, time.Now().UnixMilli(), owner.AttemptID); e != nil {
				return e
			}
		} else if old.State != "retired" {
			if _, e := q.ExecContext(ctx, `UPDATE topic_deletions SET state='deleted',updated_at=?,observation_attempt_id=? WHERE intent_id=?`, time.Now().UnixMilli(), owner.AttemptID, old.IntentID); e != nil {
				return e
			}
			d.State = "deleted"
			d.ObservationAttemptID = owner.AttemptID
		}
		_, e := q.ExecContext(ctx, `DELETE FROM telegram_message_ownership WHERE attempt_id=? AND message_id IS NULL`, owner.AttemptID)
		return e
	})
	if err != nil {
		return TopicDeletion{}, fmt.Errorf("recording exact topic deletion observation: %w", err)
	}
	return d, nil
}
