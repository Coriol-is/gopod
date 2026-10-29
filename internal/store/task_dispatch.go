package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Managed task dispatch (CP-004/021 plan PR 2). These helpers are the ledger
// side of "dispatch an already persisted turn": the queue calls them around the
// handler, and every state change that must be atomic is one transaction with no
// spawn, network or wait inside it.
//
// Single-controller assumption: exactly one gopod process owns the database and
// the task containers. A held reservation is therefore reconstructed from this
// ledger alone after restart; nothing here coordinates two live controllers.
//
// The spawn marker is the proof boundary. A runtime MUST commit BeginTaskSpawn
// before it requests native execution. Hence:
//   - queued or reserved: no spawn was ever requested (proven not started);
//   - running, cancel_requested or execution_unknown: execution may have begun,
//     and only observed local exit or operator reconciliation releases capacity.

// DispatchSnapshot is the provider/model/profile frozen onto an attempt when it
// is reserved, after the chat slot is acquired and before any spawn.
type DispatchSnapshot struct {
	Provider, Model, ProfileFingerprint string
}

// ReserveKind is the outcome of ReserveTaskAttempt.
type ReserveKind string

const (
	// ReserveNew: capacity is now held and the snapshot persisted.
	ReserveNew ReserveKind = "reserved"
	// ReserveResume: already reserved and not spawned (crash before the marker);
	// the original snapshot stays.
	ReserveResume ReserveKind = "resume"
	// ReserveRejected: not dispatchable; Reason is a fixed code. An unstarted run
	// that failed a recheck is cancelled in the same transaction.
	ReserveRejected ReserveKind = "rejected"
)

// ReserveResult carries the rows as committed.
type ReserveResult struct {
	Kind    ReserveKind
	Reason  string
	Attempt TaskAttempt
	Run     TaskRun
}

// frozenResultQ returns the CP-002 result frozen for a turn, if any.
func frozenResultQ(ctx context.Context, q taskSQL, turnID int64) (string, error) {
	var id string
	err := q.QueryRowContext(ctx, `SELECT result_id FROM turn_result_members WHERE turn_id=? LIMIT 1`, turnID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return id, err
}

func attemptQ(ctx context.Context, q taskSQL, id string) (TaskAttempt, error) {
	a, err := scanAttempt(q.QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM task_attempts WHERE attempt_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TaskAttempt{}, ErrAttemptNotFound
	}
	return a, err
}

// ReserveTaskAttempt is the pre-spawn gate. In one transaction it rechecks that
// the work is still dispatchable (no stop request, no frozen result, definition
// not deleted or expired at now, ScopeRef still valid) and then moves the
// attempt queued -> reserved, persisting the provider snapshot and holding
// capacity. Any commit failure means the caller must not spawn.
func (s *Store) ReserveTaskAttempt(ctx context.Context, attemptID string, snap DispatchSnapshot, now int64) (ReserveResult, error) {
	if snap.Provider == "" {
		return ReserveResult{}, fmt.Errorf("%w: reserving an attempt needs its provider snapshot", ErrInvalidTransition)
	}
	var out ReserveResult
	_, err := s.withTx(ctx, func(q taskSQL) error {
		a, e := attemptQ(ctx, q, attemptID)
		if e != nil {
			return e
		}
		r, e := readRunQ(ctx, q, a.RunID)
		if e != nil {
			return e
		}
		out = ReserveResult{Kind: ReserveRejected, Reason: "not_dispatchable", Attempt: a, Run: r}
		if frozen, e := frozenResultQ(ctx, q, a.TurnID); e != nil || frozen != "" {
			out.Reason = "result_frozen"
			return e
		}
		if r.State != ExecQueued || r.StopRequestedAt != 0 {
			return nil
		}
		switch a.State {
		case ExecReserved:
			out.Kind, out.Reason = ReserveResume, ""
			return nil
		case ExecQueued:
		default:
			return nil
		}
		if reason, e := dispatchRecheckQ(ctx, q, r, now); e != nil {
			return e
		} else if reason != "" {
			out.Reason = reason
			ok, e := cancelUnstartedAttemptQ(ctx, q, a, now)
			if e != nil || !ok {
				out.Reason = "not_dispatchable"
				return e
			}
			if _, e = q.ExecContext(ctx, `UPDATE task_runs SET execution_state='cancelled', failure_code=?, settled_at=? WHERE run_id=? AND execution_state='queued'`, reason, now, r.ID); e != nil {
				return e
			}
			out.Attempt.State, out.Run.State = ExecCancelled, ExecCancelled
			return nil
		}
		opts := TransitionOptions{Provider: snap.Provider, Model: snap.Model, ProfileFingerprint: snap.ProfileFingerprint}
		if e = transitionAttemptQ(ctx, q, a.ID, ExecQueued, ExecReserved, opts, now); e != nil {
			return e
		}
		out.Kind, out.Reason = ReserveNew, ""
		out.Attempt, e = attemptQ(ctx, q, a.ID)
		return e
	})
	if err != nil {
		return ReserveResult{}, fmt.Errorf("store: ReserveTaskAttempt: %w", err)
	}
	return out, nil
}

// dispatchRecheckQ returns a fixed reason code when an unstarted run must not
// start, or "" when it may.
func dispatchRecheckQ(ctx context.Context, q taskSQL, r TaskRun, now int64) (string, error) {
	t, err := readManagedTaskQ(ctx, q, r.TaskID)
	if err != nil {
		return "", err
	}
	switch {
	case t.Tombstoned():
		return "task_deleted", nil
	case t.ExpiresAt != 0 && now >= t.ExpiresAt:
		return "expired", nil
	}
	if err = validateScopeQ(ctx, q, r.Conversation.Scope); err != nil {
		if ctx.Err() != nil {
			return "", err
		}
		return "scope_changed", nil
	}
	return "", nil
}

// BeginTaskSpawn is the spawn marker: reserved -> running for the attempt and
// queued -> running for the run, atomically. It must be committed before native
// execution is requested; if it fails (for example a stop request won the race)
// the runtime must not spawn.
func (s *Store) BeginTaskSpawn(ctx context.Context, attemptID string) error {
	now := time.Now().UnixMilli()
	_, err := s.withTx(ctx, func(q taskSQL) error {
		a, e := attemptQ(ctx, q, attemptID)
		if e != nil {
			return e
		}
		if e = transitionAttemptQ(ctx, q, attemptID, ExecReserved, ExecRunning, TransitionOptions{}, now); e != nil {
			return e
		}
		return transitionRunQ(ctx, q, a.RunID, ExecQueued, ExecRunning, TransitionOptions{}, now)
	})
	if err != nil {
		return fmt.Errorf("store: BeginTaskSpawn: %w", err)
	}
	return nil
}

// SettleKind classifies how a managed handler call ended.
type SettleKind string

const (
	// SettleExited: handler returned nil and the owned local exit was observed.
	SettleExited SettleKind = "exited"
	// SettleFailed: handler failed after the spawn marker and exit was observed.
	SettleFailed SettleKind = "failed"
	// SettleStopped: the user/operator stop ended the run and exit was observed.
	SettleStopped SettleKind = "stopped"
	// SettleInterrupted: shutdown cut the run and exit was observed.
	SettleInterrupted SettleKind = "interrupted"
	// SettleUnknown: the handler returned (or timed out) without proof of exit.
	// A closed attachment or a timeout is not that proof.
	SettleUnknown SettleKind = "unknown"
	// SettleNotStarted: the attempt gave up before the spawn marker was committed.
	SettleNotStarted SettleKind = "not_started"
)

// Settlement is the host observation behind a settle. Evidence is required for
// every kind that ends in a terminal state.
type Settlement struct {
	Kind        SettleKind
	Evidence    string
	FailureCode string
}

// SettleTaskAttempt applies a Settlement to the attempt and its run in one
// transaction and returns the attempt's resulting state. It is idempotent for an
// already terminal attempt. A cancel request that landed during the run wins
// over a success report: final text from a cancellation race cannot fabricate a
// completed status.
func (s *Store) SettleTaskAttempt(ctx context.Context, attemptID string, st Settlement) (ExecState, error) {
	if st.Kind != SettleUnknown && st.Evidence == "" {
		return "", fmt.Errorf("%w: %s settlement needs host evidence", ErrInvalidTransition, st.Kind)
	}
	var final ExecState
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var e error
		final, e = settleQ(ctx, q, attemptID, st, time.Now().UnixMilli())
		return e
	})
	if err != nil {
		return "", fmt.Errorf("store: SettleTaskAttempt: %w", err)
	}
	return final, nil
}

func settleQ(ctx context.Context, q taskSQL, attemptID string, st Settlement, now int64) (ExecState, error) {
	a, err := attemptQ(ctx, q, attemptID)
	if err != nil {
		return "", err
	}
	r, err := readRunQ(ctx, q, a.RunID)
	if err != nil {
		return "", err
	}
	if a.State.terminal() {
		return a.State, nil
	}
	opts := TransitionOptions{Evidence: st.Evidence, FailureCode: st.FailureCode}
	// step moves attempt and run one edge, keeping them in lockstep.
	step := func(to ExecState, o TransitionOptions) error {
		if !CanAttemptTransition(a.State, to) || !CanRunTransition(r.State, to) {
			return fmt.Errorf("%w: attempt %s / run %s -> %s", ErrInvalidTransition, a.State, r.State, to)
		}
		if needsEvidence(a.State, to) && o.Evidence == "" {
			return fmt.Errorf("%w: %s -> %s needs host evidence", ErrInvalidTransition, a.State, to)
		}
		if e := transitionAttemptQ(ctx, q, a.ID, a.State, to, o, now); e != nil {
			return e
		}
		if e := transitionRunQ(ctx, q, r.ID, r.State, to, o, now); e != nil {
			return e
		}
		a.State, r.State = to, to
		return nil
	}
	switch st.Kind {
	case SettleNotStarted:
		if a.State != ExecQueued && a.State != ExecReserved {
			return "", ErrTransitionConflict
		}
		if r.State != ExecQueued {
			return "", ErrTransitionConflict
		}
		if opts.FailureCode == "" {
			opts.FailureCode = "not_started"
		}
		if err = step(ExecFailed, opts); err != nil {
			return "", err
		}
		return a.State, nil
	case SettleUnknown:
		if opts.FailureCode == "" {
			opts.FailureCode = "exit_not_proven"
		}
		if a.State == ExecExecutionUnknown {
			return a.State, nil
		}
		// The run may lag the attempt only if a writer bypassed the marker; both
		// must be able to reach unknown.
		if err = step(ExecExecutionUnknown, opts); err != nil {
			return "", err
		}
		return a.State, nil
	case SettleExited, SettleFailed, SettleStopped, SettleInterrupted:
	default:
		return "", fmt.Errorf("%w: settle kind %q", ErrInvalidTransition, st.Kind)
	}
	target := map[SettleKind]ExecState{SettleExited: ExecCompleted, SettleFailed: ExecFailed, SettleStopped: ExecCancelled, SettleInterrupted: ExecInterrupted}[st.Kind]
	if a.State == ExecCancelRequested {
		target = ExecCancelled // the stop request wins the race
	}
	if a.State == ExecRunning && target == ExecCancelled {
		if err = step(ExecCancelRequested, TransitionOptions{}); err != nil {
			return "", err
		}
	}
	if opts.FailureCode == "" && target == ExecFailed {
		opts.FailureCode = "execution_failed"
	}
	if err = step(target, opts); err != nil {
		return "", err
	}
	return a.State, nil
}

// RecoveryAction tells the queue what boot recovery may do with a managed turn.
type RecoveryAction string

const (
	// RecoverDispatch: proven never started; dispatch it normally.
	RecoverDispatch RecoveryAction = "dispatch"
	// RecoverHold: execution may have begun. It is (now) execution_unknown with
	// its reservation held; never run the model again.
	RecoverHold RecoveryAction = "hold"
	// RecoverFrozen: a result is frozen. Recover publication only; also held as
	// unknown until exit evidence, because a frozen result is not proof of exit.
	RecoverFrozen RecoveryAction = "frozen"
	// RecoverSettled: the ledger already settled; only close the turn row.
	RecoverSettled RecoveryAction = "settled"
)

// RecoveryPlan is the committed decision for one managed turn.
type RecoveryPlan struct {
	Action   RecoveryAction
	Attempt  TaskAttempt
	Run      TaskRun
	ResultID string
}

// RecoverTaskAttempt classifies a managed turn found unfinished at boot and
// applies the conservative state change in one transaction. It returns
// ErrAttemptNotFound for turns that are not managed attempts. Recovery of a
// possibly started attempt moves it to execution_unknown (never resumes it) and
// keeps its capacity reservation; it is idempotent across repeated restarts.
func (s *Store) RecoverTaskAttempt(ctx context.Context, turnID int64) (RecoveryPlan, error) {
	var out RecoveryPlan
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var attemptID string
		e := q.QueryRowContext(ctx, `SELECT attempt_id FROM task_attempts WHERE turn_id=?`, turnID).Scan(&attemptID)
		if errors.Is(e, sql.ErrNoRows) {
			return ErrAttemptNotFound
		}
		if e != nil {
			return e
		}
		a, e := attemptQ(ctx, q, attemptID)
		if e != nil {
			return e
		}
		r, e := readRunQ(ctx, q, a.RunID)
		if e != nil {
			return e
		}
		frozen, e := frozenResultQ(ctx, q, turnID)
		if e != nil {
			return e
		}
		out = RecoveryPlan{Attempt: a, Run: r, ResultID: frozen}
		switch {
		case a.State.terminal():
			out.Action = RecoverSettled
			return nil
		case frozen == "" && (a.State == ExecQueued || a.State == ExecReserved) && r.State == ExecQueued:
			out.Action = RecoverDispatch
			return nil
		}
		// Possibly started (or frozen without proof of exit): unknown, never replay.
		if a.State != ExecExecutionUnknown {
			if _, e = settleQ(ctx, q, attemptID, Settlement{Kind: SettleUnknown, FailureCode: "restart_recovery"}, time.Now().UnixMilli()); e != nil {
				return e
			}
			if out.Attempt, e = attemptQ(ctx, q, attemptID); e != nil {
				return e
			}
			if out.Run, e = readRunQ(ctx, q, a.RunID); e != nil {
				return e
			}
		}
		out.Action = RecoverHold
		if frozen != "" {
			out.Action = RecoverFrozen
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrAttemptNotFound) {
			return RecoveryPlan{}, ErrAttemptNotFound
		}
		return RecoveryPlan{}, fmt.Errorf("store: RecoverTaskAttempt: %w", err)
	}
	return out, nil
}

// HeldReservation is a capacity reservation that must be rebuilt after restart.
type HeldReservation struct {
	AttemptID, RunID string
	TurnID           int64
	Folder           string
	State            ExecState
}

// HeldTaskReservations lists reservations that still account for possibly
// running work: held, and past the spawn marker (a merely reserved attempt is
// redispatched, not held). Rebuild these before admitting any work into the
// affected namespace; they consume the existing global budget.
func (s *Store) HeldTaskReservations(ctx context.Context) ([]HeldReservation, error) {
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT a.attempt_id, a.run_id, a.turn_id, r.chat_folder, a.execution_state
 FROM task_attempts a JOIN task_runs r ON r.run_id=a.run_id
 WHERE a.reservation_state='held' AND a.execution_state<>'reserved' ORDER BY a.created_at, a.attempt_id`)
	if err != nil {
		return nil, fmt.Errorf("store: HeldTaskReservations: %w", err)
	}
	defer rows.Close()
	var out []HeldReservation
	for rows.Next() {
		var h HeldReservation
		var st string
		if err = rows.Scan(&h.AttemptID, &h.RunID, &h.TurnID, &h.Folder, &st); err != nil {
			return nil, fmt.Errorf("store: HeldTaskReservations: %w", err)
		}
		h.State = ExecState(st)
		out = append(out, h)
	}
	return out, rows.Err()
}
