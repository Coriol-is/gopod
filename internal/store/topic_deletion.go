package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var (
	ErrTopicDeleting      = errors.New("store: topic deletion is unresolved")
	ErrTopicDeletionState = errors.New("store: stale topic deletion intent or invalid transition")
)

// TopicDeletion is the durable target of one confirmed private-topic deletion.
// IntentID is host generated; it must accompany every outcome/cleanup write.
// A resumed deleting/uncertain intent is NOT permission to repeat the API call:
// the original request may already have succeeded.
type TopicDeletion struct {
	Scope    ScopeRef
	ThreadID int
	IntentID string
	State    string // deleting | uncertain | deleted | retired | refused
}

func validatePrivateTopicQ(ctx context.Context, q taskSQL, scope ScopeRef, thread int) error {
	if err := validateScopeQ(ctx, q, scope); err != nil {
		return err
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(scope.JID, "tg:"), 10, 64)
	if thread <= 0 || !strings.HasPrefix(scope.JID, "tg:") || err != nil || id <= 0 {
		return ErrInvalidConversation
	}
	return nil
}

func readTopicDeletionQ(ctx context.Context, q taskSQL, scope ScopeRef, thread int) (TopicDeletion, error) {
	d := TopicDeletion{Scope: scope, ThreadID: thread}
	err := q.QueryRowContext(ctx, `SELECT intent_id,registration_revision,chat_jid,folder,state FROM topic_deletions
 WHERE namespace_id=? AND namespace_epoch=? AND thread_id=?`, scope.NamespaceID, scope.NamespaceEpoch, thread).
		Scan(&d.IntentID, &d.Scope.RegistrationRevision, &d.Scope.JID, &d.Scope.Folder, &d.State)
	return d, err
}

// GetTopicDeletion reads an intent without creating any state. Authorization is
// the current registration; a changed registration cannot operate an old intent.
func (s *Store) GetTopicDeletion(ctx context.Context, scope ScopeRef, thread int) (TopicDeletion, bool, error) {
	var d TopicDeletion
	found := false
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validatePrivateTopicQ(ctx, q, scope, thread); err != nil {
			return err
		}
		var err error
		d, err = readTopicDeletionQ(ctx, q, scope, thread)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if d.Scope != scope {
			return ErrStaleScope
		}
		found = true
		return nil
	})
	return d, found, err
}

// BeginTopicDeletion records an intent after the caller has validated its actor-
// bound confirmation. created=false means an existing intent was found: do not
// issue a second deleteForumTopic request. A definite refusal allows a fresh
// confirmation to create a new intent; old callbacks cannot settle that intent.
// The orchestrator must stop admission and quiesce the exact conversation before
// calling Telegram. This store primitive does not prove process termination.
func (s *Store) BeginTopicDeletion(ctx context.Context, scope ScopeRef, thread int) (d TopicDeletion, created bool, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if err := validatePrivateTopicQ(ctx, q, scope, thread); err != nil {
			return err
		}
		old, err := readTopicDeletionQ(ctx, q, scope, thread)
		if err == nil {
			if old.Scope != scope {
				return ErrStaleScope
			}
			if old.State != "refused" {
				d = old
				return nil
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		id, err := newUUID()
		if err != nil {
			return err
		}
		d = TopicDeletion{Scope: scope, ThreadID: thread, IntentID: id, State: "deleting"}
		_, err = q.ExecContext(ctx, `INSERT INTO topic_deletions(namespace_id,namespace_epoch,thread_id,intent_id,registration_revision,chat_jid,folder,state,updated_at,dispatch_started)
 VALUES(?,?,?,?,?,?,?,'deleting',?,0) ON CONFLICT(namespace_id,namespace_epoch,thread_id) DO UPDATE SET
 intent_id=excluded.intent_id,state=excluded.state,updated_at=excluded.updated_at,dispatch_started=0`, scope.NamespaceID, scope.NamespaceEpoch, thread, id, scope.RegistrationRevision, scope.JID, scope.Folder, time.Now().UnixMilli())
		created = err == nil
		return err
	})
	if err != nil {
		return TopicDeletion{}, false, fmt.Errorf("beginning topic deletion: %w", err)
	}
	return d, created, nil
}

func ownedTopicDeletionQ(ctx context.Context, q taskSQL, expected TopicDeletion) (TopicDeletion, error) {
	if err := validatePrivateTopicQ(ctx, q, expected.Scope, expected.ThreadID); err != nil {
		return TopicDeletion{}, err
	}
	d, err := readTopicDeletionQ(ctx, q, expected.Scope, expected.ThreadID)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ErrTopicDeletionState
	}
	if err != nil {
		return d, err
	}
	if d.Scope != expected.Scope || d.IntentID != expected.IntentID {
		return d, ErrTopicDeletionState
	}
	return d, nil
}

// RecordTopicDeletionOutcome accepts only deleted (confirmed API success), refused
// (a definite refusal of this request), or uncertain (transport ambiguity).
// Once uncertain, a later refusal is not proof the original request failed;
// only positive deletion evidence may advance it. It never clears context.
func (s *Store) RecordTopicDeletionOutcome(ctx context.Context, expected TopicDeletion, outcome string) error {
	if outcome != "deleted" && outcome != "refused" && outcome != "uncertain" {
		return ErrTopicDeletionState
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		d, err := ownedTopicDeletionQ(ctx, q, expected)
		if err != nil {
			return err
		}
		if d.State == outcome || (d.State == "retired" && outcome == "deleted") {
			return nil
		}
		if d.State != "deleting" && !(d.State == "uncertain" && outcome == "deleted") {
			return ErrTopicDeletionState
		}
		_, err = q.ExecContext(ctx, `UPDATE topic_deletions SET state=?,updated_at=? WHERE intent_id=?`, outcome, time.Now().UnixMilli(), d.IntentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("recording topic deletion outcome: %w", err)
	}
	return nil
}

// RetireDeletedTopic atomically removes resumability after confirmed deletion.
// Refuse unfinished turns and every execution claim, including execution_unknown.
// The caller must also hold its conversation admission fence and prove native
// process exit; terminal database rows alone are not that proof. Audit turns,
// memories, shared files, credentials and other topics are preserved.
func (s *Store) RetireDeletedTopic(ctx context.Context, expected TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		d, err := ownedTopicDeletionQ(ctx, q, expected)
		if err != nil {
			return err
		}
		if d.State == "retired" {
			return nil
		}
		if d.State != "deleted" {
			return ErrTopicDeletionState
		}
		var busy int
		if err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE chat_folder=? AND thread_id=? AND status NOT IN ('done','failed','stopped'))
 OR EXISTS(SELECT 1 FROM execution_claims c JOIN execution_sessions s ON s.session_id=c.session_id WHERE s.namespace_id=? AND s.namespace_epoch=? AND s.thread_id=?)`,
			d.Scope.Folder, d.ThreadID, d.Scope.NamespaceID, d.Scope.NamespaceEpoch, d.ThreadID).Scan(&busy); err != nil {
			return err
		}
		if busy != 0 {
			return ErrSessionBusy
		}
		args := []any{d.Scope.NamespaceID, d.Scope.NamespaceEpoch, d.ThreadID}
		if _, err = q.ExecContext(ctx, `DELETE FROM native_contexts WHERE session_id IN (SELECT session_id FROM execution_sessions WHERE namespace_id=? AND namespace_epoch=? AND thread_id=?)`, args...); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE execution_sessions SET lifecycle='retired' WHERE namespace_id=? AND namespace_epoch=? AND thread_id=?`, args...); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE conversation_bindings SET lifecycle='retired',revision=revision+1,updated_at=? WHERE namespace_id=? AND namespace_epoch=? AND thread_id=?`, time.Now().UnixMilli(), args[0], args[1], args[2]); err != nil {
			return err
		}
		// D056 freezes the legacy table once durable mapping is adopted.
		if _, err = q.ExecContext(ctx, `DELETE FROM sessions WHERE chat_folder=? AND thread_id=? AND NOT EXISTS(SELECT 1 FROM router_state WHERE key='session_mapping' AND value='durable')`, d.Scope.Folder, d.ThreadID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `DELETE FROM conversation_history WHERE chat_folder=? AND thread_id=?`, d.Scope.Folder, d.ThreadID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `DELETE FROM router_state WHERE key=?`, continuityKey(d.Scope.Folder, d.ThreadID, "provider")); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value='1'`, continuityKey(d.Scope.Folder, d.ThreadID, "seeded")); err != nil {
			return err
		}
		_, err = q.ExecContext(ctx, `UPDATE topic_deletions SET state='retired',updated_at=? WHERE intent_id=?`, time.Now().UnixMilli(), d.IntentID)
		return err
	})
	if err != nil {
		return fmt.Errorf("retiring deleted topic: %w", err)
	}
	return nil
}

// topicAdmissionQ is the durable-session integration gate. Legacy runner, queue,
// output and callback fences must be wired before exposing a deletion command.
func topicAdmissionQ(ctx context.Context, q taskSQL, scope ScopeRef, thread int) error {
	d, err := readTopicDeletionQ(ctx, q, scope, thread)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if d.State == "refused" {
		return nil
	}
	if d.State == "deleted" || d.State == "retired" {
		return ErrConversationRetired
	}
	return ErrTopicDeleting
}

// topicFolderAdmissionQ fences legacy write paths using current registration.
// Call inside the same transaction as the write to prevent resurrection races.
func topicFolderAdmissionQ(ctx context.Context, q taskSQL, folder string, thread int) error {
	var state string
	err := q.QueryRowContext(ctx, `SELECT d.state FROM topic_deletions d
 JOIN registered_chats r ON r.namespace_id=d.namespace_id AND r.namespace_epoch=d.namespace_epoch AND r.folder=d.folder
 WHERE r.folder=? AND d.thread_id=?`, folder, thread).Scan(&state)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if state == "refused" {
		return nil
	}
	if state == "deleted" || state == "retired" {
		return ErrConversationRetired
	}
	return ErrTopicDeleting
}

// PendingTopicDeletions returns durable fences to restore before queue/polling
// recovery. Refused and fully retired intents need no transient runtime lease.
// Recorded registration identity is retained even when current metadata drifted.
func (s *Store) PendingTopicDeletions(ctx context.Context) ([]TopicDeletion, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT namespace_id,namespace_epoch,registration_revision,chat_jid,folder,thread_id,intent_id,state FROM topic_deletions WHERE state IN ('deleting','uncertain','deleted') ORDER BY namespace_id,namespace_epoch,thread_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopicDeletion
	for rows.Next() {
		var d TopicDeletion
		if err := rows.Scan(&d.Scope.NamespaceID, &d.Scope.NamespaceEpoch, &d.Scope.RegistrationRevision, &d.Scope.JID, &d.Scope.Folder, &d.ThreadID, &d.IntentID, &d.State); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// TopicDeletionTransportIdle preserves issued-request evidence. Neither pending
// cancellation nor a returned handler can prove sending/uncertain work absent.
func (s *Store) TopicDeletionTransportIdle(ctx context.Context, expected TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if _, err := ownedTopicDeletionQ(ctx, q, expected); err != nil {
			return err
		}
		var busy bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE g.namespace_id=? AND g.namespace_epoch=? AND g.thread_id=? AND p.state IN ('sending','uncertain'))`, expected.Scope.NamespaceID, expected.Scope.NamespaceEpoch, expected.ThreadID).Scan(&busy)
		if err != nil {
			return err
		}
		if busy {
			return ErrTopicTransportBusy
		}
		return nil
	})
	return err
}

var ErrTopicTransportBusy = errors.New("store: topic has in-flight or uncertain transport")

// TopicDeletionWorkIdle also checks durable rows, covering accepted input that
// was committed before the intent but has not reached the in-memory queue yet.
func (s *Store) TopicDeletionWorkIdle(ctx context.Context, expected TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if _, err := ownedTopicDeletionQ(ctx, q, expected); err != nil {
			return err
		}
		var busy bool
		err := q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE chat_folder=? AND thread_id=? AND status NOT IN ('done','failed','stopped')) OR EXISTS(SELECT 1 FROM execution_claims c JOIN execution_sessions s ON s.session_id=c.session_id WHERE s.namespace_id=? AND s.namespace_epoch=? AND s.thread_id=?)`, expected.Scope.Folder, expected.ThreadID, expected.Scope.NamespaceID, expected.Scope.NamespaceEpoch, expected.ThreadID).Scan(&busy)
		if err != nil {
			return err
		}
		if busy {
			return ErrSessionBusy
		}
		return nil
	})
	return err
}

// WithRefusedTopicFence serializes a runtime-only release with durable intent
// creation/registration changes. release must not call back into Store.
func (s *Store) WithRefusedTopicFence(ctx context.Context, expected TopicDeletion, release func() error) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		d, err := ownedTopicDeletionQ(ctx, q, expected)
		if err != nil {
			return err
		}
		if d.State != "refused" {
			return ErrTopicDeletionState
		}
		return release()
	})
	return err
}

// ClaimTopicDeletionDispatch records the irreversible HTTP boundary once. Existing
// pre-migration intents default to dispatched; they never acquire retry authority.
func (s *Store) ClaimTopicDeletionDispatch(ctx context.Context, expected TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		d, err := ownedTopicDeletionQ(ctx, q, expected)
		if err != nil {
			return err
		}
		if d.State != "deleting" {
			return ErrTopicDeletionState
		}
		res, err := q.ExecContext(ctx, `UPDATE topic_deletions SET dispatch_started=1,updated_at=? WHERE intent_id=? AND dispatch_started=0`, time.Now().UnixMilli(), d.IntentID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrTopicDeletionState
		}
		return nil
	})
	return err
}

// RefuseUnissuedTopicDeletion cannot reclassify an issued or historical request.
func (s *Store) RefuseUnissuedTopicDeletion(ctx context.Context, expected TopicDeletion) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		d, err := ownedTopicDeletionQ(ctx, q, expected)
		if err != nil {
			return err
		}
		if d.State != "deleting" {
			return ErrTopicDeletionState
		}
		res, err := q.ExecContext(ctx, `UPDATE topic_deletions SET state='refused',updated_at=? WHERE intent_id=? AND dispatch_started=0`, time.Now().UnixMilli(), d.IntentID)
		if err != nil {
			return err
		}
		n, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if n != 1 {
			return ErrTopicDeletionState
		}
		return nil
	})
	return err
}
