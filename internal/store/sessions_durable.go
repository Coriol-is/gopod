package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const (
	SessionMappingLegacy  = "legacy"
	SessionMappingDurable = "durable"
	sessionMappingKey     = "session_mapping"           // router_state
	sessionMappingReqKey  = "session_mapping_requested" // router_state, "durable" or ""
)

var (
	ErrConversationRetired = errors.New("store: conversation binding is retired")
	ErrStaleContextEpoch   = errors.New("store: attempt context epoch is not current")
	ErrAttemptBound        = errors.New("store: turn already bound to a different session")
	ErrSessionBusy         = errors.New("store: session already holds an execution claim")
	ErrAdoptionBusy        = errors.New("store: unfinished turns exist; adoption needs a quiesced controller")
)

// ConversationBinding maps one conversation (registration scope + thread) to
// its active execution session.
type ConversationBinding struct {
	NamespaceID     string
	NamespaceEpoch  int64
	ThreadID        int
	ActiveSessionID string
	ContextEpoch    int64 // of the active session
	WorkspaceID     string
	Revision        int64
	Lifecycle       string // "active" | "retired"
}

// ExecutionSession is one durable, host-owned execution identity.
type ExecutionSession struct {
	SessionID             string
	NamespaceID           string
	NamespaceEpoch        int64
	RegistrationRevision  int64
	Folder                string
	ThreadID              int
	Kind                  string // "conversation" | "task_attempt"
	ContextEpoch          int64
	WorkspaceID           string
	Lifecycle             string // "active" | "archived" | "retired"
	CreatedAt, ArchivedAt int64
}

// AttemptBinding records which session and context epoch one turn ran under.
type AttemptBinding struct {
	TurnID             int64
	SessionID          string
	ContextEpoch       int64
	Provider, Model    string
	ProfileFingerprint string
	RuntimeGeneration  string
	CreatedAt          int64
}

// ResolveConversation returns the active binding for (scope, thread), creating
// the session and binding on first use. Creating a binding at a new namespace
// epoch retires the active bindings (and their sessions) of older epochs.
func (s *Store) ResolveConversation(ctx context.Context, scope ScopeRef, thread int) (ConversationBinding, error) {
	var out ConversationBinding
	_, err := s.withTx(ctx, func(q taskSQL) error {
		b, _, e := resolveConversationQ(ctx, q, scope, thread)
		out = b
		return e
	})
	if err != nil {
		return ConversationBinding{}, fmt.Errorf("resolving conversation: %w", err)
	}
	return out, nil
}

// resolveConversationQ is ResolveConversation on an explicit handle; created
// reports whether a new execution session was inserted.
func resolveConversationQ(ctx context.Context, q taskSQL, scope ScopeRef, thread int) (ConversationBinding, bool, error) {
	if thread < 0 {
		return ConversationBinding{}, false, ErrInvalidConversation
	}
	if err := validateScopeQ(ctx, q, scope); err != nil {
		return ConversationBinding{}, false, err
	}
	b, err := readBindingQ(ctx, q, scope.NamespaceID, scope.NamespaceEpoch, thread)
	if err == nil {
		if b.Lifecycle != "active" {
			return ConversationBinding{}, false, ErrConversationRetired
		}
		return b, false, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ConversationBinding{}, false, err
	}
	now := time.Now().UnixMilli()
	if _, err = q.ExecContext(ctx, `UPDATE execution_sessions SET lifecycle='retired' WHERE session_id IN (
 SELECT active_session_id FROM conversation_bindings WHERE namespace_id=? AND namespace_epoch<? AND lifecycle='active')`,
		scope.NamespaceID, scope.NamespaceEpoch); err != nil {
		return ConversationBinding{}, false, fmt.Errorf("retiring old-epoch sessions: %w", err)
	}
	if _, err = q.ExecContext(ctx, `UPDATE conversation_bindings SET lifecycle='retired',revision=revision+1,updated_at=?
 WHERE namespace_id=? AND namespace_epoch<? AND lifecycle='active'`, now, scope.NamespaceID, scope.NamespaceEpoch); err != nil {
		return ConversationBinding{}, false, fmt.Errorf("retiring old-epoch bindings: %w", err)
	}
	id, err := newUUID()
	if err != nil {
		return ConversationBinding{}, false, err
	}
	workspace := "legacy:" + scope.Folder
	if _, err = q.ExecContext(ctx, `INSERT INTO execution_sessions(session_id,namespace_id,namespace_epoch,registration_revision,folder,thread_id,kind,context_epoch,workspace_id,lifecycle,created_at)
 VALUES(?,?,?,?,?,?,'conversation',1,?,'active',?)`,
		id, scope.NamespaceID, scope.NamespaceEpoch, scope.RegistrationRevision, scope.Folder, thread, workspace, now); err != nil {
		return ConversationBinding{}, false, fmt.Errorf("inserting execution session: %w", err)
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO conversation_bindings(namespace_id,namespace_epoch,thread_id,active_session_id,revision,lifecycle,updated_at)
 VALUES(?,?,?,?,1,'active',?)`, scope.NamespaceID, scope.NamespaceEpoch, thread, id, now); err != nil {
		return ConversationBinding{}, false, fmt.Errorf("inserting conversation binding: %w", err)
	}
	return ConversationBinding{
		NamespaceID: scope.NamespaceID, NamespaceEpoch: scope.NamespaceEpoch, ThreadID: thread,
		ActiveSessionID: id, ContextEpoch: 1, WorkspaceID: workspace, Revision: 1, Lifecycle: "active",
	}, true, nil
}

func readBindingQ(ctx context.Context, q taskSQL, namespaceID string, epoch int64, thread int) (ConversationBinding, error) {
	var b ConversationBinding
	err := q.QueryRowContext(ctx, `SELECT b.namespace_id,b.namespace_epoch,b.thread_id,b.active_session_id,s.context_epoch,s.workspace_id,b.revision,b.lifecycle
 FROM conversation_bindings b JOIN execution_sessions s ON s.session_id=b.active_session_id
 WHERE b.namespace_id=? AND b.namespace_epoch=? AND b.thread_id=?`, namespaceID, epoch, thread).
		Scan(&b.NamespaceID, &b.NamespaceEpoch, &b.ThreadID, &b.ActiveSessionID, &b.ContextEpoch, &b.WorkspaceID, &b.Revision, &b.Lifecycle)
	return b, err
}

const executionSessionColumns = `SELECT session_id,namespace_id,namespace_epoch,registration_revision,folder,thread_id,kind,context_epoch,workspace_id,lifecycle,created_at,IFNULL(archived_at,0) FROM execution_sessions WHERE session_id=?`

func readExecutionSessionQ(ctx context.Context, q taskSQL, sessionID string) (ExecutionSession, error) {
	var e ExecutionSession
	err := q.QueryRowContext(ctx, executionSessionColumns, sessionID).Scan(&e.SessionID, &e.NamespaceID, &e.NamespaceEpoch,
		&e.RegistrationRevision, &e.Folder, &e.ThreadID, &e.Kind, &e.ContextEpoch, &e.WorkspaceID, &e.Lifecycle, &e.CreatedAt, &e.ArchivedAt)
	return e, err
}

// GetExecutionSession returns one session by id; a missing id wraps sql.ErrNoRows.
func (s *Store) GetExecutionSession(ctx context.Context, sessionID string) (ExecutionSession, error) {
	e, err := readExecutionSessionQ(ctx, s.taskDB(ctx), sessionID)
	if err != nil {
		return ExecutionSession{}, fmt.Errorf("getting execution session %q: %w", sessionID, err)
	}
	return e, nil
}

// ownedActiveSessionQ validates scope and returns the session only when it is
// active and belongs to the scope's namespace id and epoch. Authorization comes
// from the host-owned scope, never from the caller-supplied session id alone.
func ownedActiveSessionQ(ctx context.Context, q taskSQL, scope ScopeRef, sessionID string) (ExecutionSession, error) {
	if err := validateScopeQ(ctx, q, scope); err != nil {
		return ExecutionSession{}, err
	}
	e, err := readExecutionSessionQ(ctx, q, sessionID)
	if errors.Is(err, sql.ErrNoRows) {
		return ExecutionSession{}, fmt.Errorf("execution session %q not found: %w", sessionID, ErrStaleScope)
	}
	if err != nil {
		return ExecutionSession{}, err
	}
	if e.Lifecycle != "active" || e.NamespaceID != scope.NamespaceID || e.NamespaceEpoch != scope.NamespaceEpoch {
		return ExecutionSession{}, fmt.Errorf("execution session %q is not active in this scope: %w", sessionID, ErrStaleScope)
	}
	return e, nil
}

// ownedTurnQ refuses a turn that is missing or belongs to another folder, so a
// session can only be linked to turns of its own chat.
func ownedTurnQ(ctx context.Context, q taskSQL, scope ScopeRef, turnID int64) error {
	var folder string
	err := q.QueryRowContext(ctx, `SELECT chat_folder FROM turns WHERE id=?`, turnID).Scan(&folder)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("turn %d not found: %w", turnID, ErrStaleScope)
	}
	if err != nil {
		return fmt.Errorf("reading turn %d owner: %w", turnID, err)
	}
	if folder != scope.Folder {
		return fmt.Errorf("turn %d belongs to another folder: %w", turnID, ErrStaleScope)
	}
	return nil
}

// GetNativeContext returns the provider-native context id for one session epoch.
func (s *Store) GetNativeContext(ctx context.Context, sessionID string, epoch int64, provider string) (string, bool, error) {
	var id string
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT native_id FROM native_contexts WHERE session_id=? AND context_epoch=? AND provider=?`,
		sessionID, epoch, provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("getting native context: %w", err)
	}
	return id, true, nil
}

// PutNativeContext upserts the native id for the session's current epoch.
func (s *Store) PutNativeContext(ctx context.Context, scope ScopeRef, sessionID string, epoch int64, provider, nativeID string) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		e, err := ownedActiveSessionQ(ctx, q, scope, sessionID)
		if err != nil {
			return err
		}
		if epoch != e.ContextEpoch {
			return ErrStaleContextEpoch
		}
		_, err = q.ExecContext(ctx, `INSERT INTO native_contexts(session_id,context_epoch,provider,native_id,updated_at) VALUES(?,?,?,?,?)
 ON CONFLICT(session_id,context_epoch,provider) DO UPDATE SET native_id=excluded.native_id,updated_at=excluded.updated_at`,
			sessionID, epoch, provider, nativeID, time.Now().UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("putting native context: %w", err)
	}
	return nil
}

// DeleteNativeContext removes one native id; a missing row is not an error.
func (s *Store) DeleteNativeContext(ctx context.Context, scope ScopeRef, sessionID string, epoch int64, provider string) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if _, err := ownedActiveSessionQ(ctx, q, scope, sessionID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `DELETE FROM native_contexts WHERE session_id=? AND context_epoch=? AND provider=?`, sessionID, epoch, provider)
		return err
	})
	if err != nil {
		return fmt.Errorf("deleting native context: %w", err)
	}
	return nil
}

// BindAttempt records the session and epoch a turn runs under. Rebinding the
// same turn is idempotent only for the same session and epoch.
func (s *Store) BindAttempt(ctx context.Context, scope ScopeRef, b AttemptBinding) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		e, err := ownedActiveSessionQ(ctx, q, scope, b.SessionID)
		if err != nil {
			return err
		}
		if err = ownedTurnQ(ctx, q, scope, b.TurnID); err != nil {
			return err
		}
		if b.ContextEpoch != e.ContextEpoch {
			return ErrStaleContextEpoch
		}
		res, err := q.ExecContext(ctx, `INSERT INTO attempt_bindings(turn_id,session_id,context_epoch,provider,model,profile_fingerprint,runtime_generation,created_at)
 VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(turn_id) DO NOTHING`,
			b.TurnID, b.SessionID, b.ContextEpoch, b.Provider, b.Model, b.ProfileFingerprint, b.RuntimeGeneration, time.Now().UnixMilli())
		if err != nil {
			return err
		}
		inserted, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if inserted == 1 {
			return nil
		}
		var sid string
		var epoch int64
		if err = q.QueryRowContext(ctx, `SELECT session_id,context_epoch FROM attempt_bindings WHERE turn_id=?`, b.TurnID).Scan(&sid, &epoch); err != nil {
			return err
		}
		if sid != b.SessionID || epoch != b.ContextEpoch {
			return ErrAttemptBound
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("binding attempt for turn %d: %w", b.TurnID, err)
	}
	return nil
}

// GetAttemptBinding returns the binding recorded for a turn, if any.
func (s *Store) GetAttemptBinding(ctx context.Context, turnID int64) (AttemptBinding, bool, error) {
	var b AttemptBinding
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT turn_id,session_id,context_epoch,provider,model,profile_fingerprint,runtime_generation,created_at
 FROM attempt_bindings WHERE turn_id=?`, turnID).
		Scan(&b.TurnID, &b.SessionID, &b.ContextEpoch, &b.Provider, &b.Model, &b.ProfileFingerprint, &b.RuntimeGeneration, &b.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return AttemptBinding{}, false, nil
	}
	if err != nil {
		return AttemptBinding{}, false, fmt.Errorf("getting attempt binding: %w", err)
	}
	return b, true, nil
}

// AcquireExecutionClaim gives turnID the single execution claim on a session.
// Re-acquiring for the same turn is idempotent. Another turn takes the claim
// over when the holder turn is terminal (done, failed, stopped) or the claim is
// execution_unknown: in P1 an unknown claim is recorded evidence and the
// runner's process proof gates re-execution (D055). Otherwise it gets
// ErrSessionBusy.
func (s *Store) AcquireExecutionClaim(ctx context.Context, scope ScopeRef, sessionID string, turnID int64) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if _, err := ownedActiveSessionQ(ctx, q, scope, sessionID); err != nil {
			return err
		}
		if err := ownedTurnQ(ctx, q, scope, turnID); err != nil {
			return err
		}
		var holder int64
		var state, holderStatus string
		err := q.QueryRowContext(ctx, `SELECT c.turn_id,c.state,IFNULL(t.status,'') FROM execution_claims c LEFT JOIN turns t ON t.id=c.turn_id WHERE c.session_id=?`,
			sessionID).Scan(&holder, &state, &holderStatus)
		switch {
		case err == nil && holder == turnID:
			return nil
		case err == nil:
			terminal := holderStatus == TurnDone || holderStatus == TurnFailed || holderStatus == TurnStopped
			if state != "execution_unknown" && !terminal {
				return ErrSessionBusy
			}
			if _, err = q.ExecContext(ctx, `DELETE FROM execution_claims WHERE session_id=?`, sessionID); err != nil {
				return fmt.Errorf("taking over execution claim from turn %d: %w", holder, err)
			}
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO execution_claims(session_id,turn_id,state,claimed_at) VALUES(?,?,'held',?)`,
			sessionID, turnID, time.Now().UnixMilli())
		return err
	})
	if err != nil {
		return fmt.Errorf("acquiring execution claim: %w", err)
	}
	return nil
}

// ExecutionClaimHolder reports the turn and state of a session's claim, if any.
func (s *Store) ExecutionClaimHolder(ctx context.Context, sessionID string) (turnID int64, state string, ok bool, err error) {
	err = s.taskDB(ctx).QueryRowContext(ctx, `SELECT turn_id,state FROM execution_claims WHERE session_id=?`, sessionID).Scan(&turnID, &state)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, "", false, nil
	}
	if err != nil {
		return 0, "", false, fmt.Errorf("reading execution claim holder: %w", err)
	}
	return turnID, state, true, nil
}

// ReleaseStaleExecutionClaims deletes every held claim. Call it only at
// startup before queue recovery, when nothing can be running: a held claim
// then belongs to a process that crashed between acquire and settle.
// execution_unknown claims are kept as evidence.
func (s *Store) ReleaseStaleExecutionClaims(ctx context.Context) (int, error) {
	res, err := s.taskDB(ctx).ExecContext(ctx, `DELETE FROM execution_claims WHERE state='held'`)
	if err != nil {
		return 0, fmt.Errorf("releasing stale execution claims: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("counting released execution claims: %w", err)
	}
	return int(n), nil
}

// SettleExecutionClaim releases the claim, or with unknown=true marks it
// execution_unknown as durable evidence that the run's exit is unproven.
func (s *Store) SettleExecutionClaim(ctx context.Context, sessionID string, turnID int64, unknown bool) error {
	stmt := `DELETE FROM execution_claims WHERE session_id=? AND turn_id=?`
	if unknown {
		stmt = `UPDATE execution_claims SET state='execution_unknown' WHERE session_id=? AND turn_id=?`
	}
	if _, err := s.taskDB(ctx).ExecContext(ctx, stmt, sessionID, turnID); err != nil {
		return fmt.Errorf("settling execution claim: %w", err)
	}
	return nil
}

// CountSessionRows counts execution_sessions and native_contexts rows.
func (s *Store) CountSessionRows(ctx context.Context) (sessions, contexts int, err error) {
	q := s.taskDB(ctx)
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM execution_sessions`).Scan(&sessions); err != nil {
		return 0, 0, fmt.Errorf("counting execution sessions: %w", err)
	}
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM native_contexts`).Scan(&contexts); err != nil {
		return 0, 0, fmt.Errorf("counting native contexts: %w", err)
	}
	return sessions, contexts, nil
}

// ClearDurableConversationContinuity is ClearConversationContinuity for the
// durable mapping: it clears the replay transcript, writes the same seeded and
// session_adopted markers, and deletes every provider's native context for the
// active session's current context epoch, all in one transaction. The legacy
// sessions table is frozen after adoption and is never touched here.
func (s *Store) ClearDurableConversationContinuity(ctx context.Context, scope ScopeRef, thread int) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		b, _, err := resolveConversationQ(ctx, q, scope, thread)
		if err != nil {
			return err
		}
		folder := scope.Folder
		if _, err = q.ExecContext(ctx, `DELETE FROM conversation_history WHERE chat_folder=? AND thread_id=?`, folder, thread); err != nil {
			return fmt.Errorf("deleting conversation history: %w", err)
		}
		const mark = `INSERT INTO router_state(key,value) VALUES(?,'1') ON CONFLICT(key) DO NOTHING`
		if _, err = q.ExecContext(ctx, mark, continuityKey(folder, thread, "seeded")); err != nil {
			return fmt.Errorf("marking continuity seeded: %w", err)
		}
		if thread == 0 {
			// Prevent a previously unused provider from re-adopting an old main session.
			for _, p := range []string{"claude", "codex"} {
				if _, err = q.ExecContext(ctx, mark, "session_adopted:"+folder+":"+p); err != nil {
					return fmt.Errorf("marking session adopted: %w", err)
				}
			}
		}
		if _, err = q.ExecContext(ctx, `DELETE FROM native_contexts WHERE session_id=? AND context_epoch=?`, b.ActiveSessionID, b.ContextEpoch); err != nil {
			return fmt.Errorf("deleting native contexts: %w", err)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("clearing durable conversation continuity: %w", err)
	}
	return nil
}
