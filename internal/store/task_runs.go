package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"
)

// ExecState is the execution state of a run or an attempt. It is independent of
// goal outcome, publication and delivery (CP-021): a completed run can have an
// unknown goal and an uncertain delivery.
type ExecState string

const (
	ExecQueued           ExecState = "queued"
	ExecReserved         ExecState = "reserved" // attempts only: capacity held before spawn
	ExecRunning          ExecState = "running"
	ExecCancelRequested  ExecState = "cancel_requested"
	ExecCompleted        ExecState = "completed"
	ExecFailed           ExecState = "failed"
	ExecCancelled        ExecState = "cancelled"
	ExecInterrupted      ExecState = "interrupted"
	ExecExecutionUnknown ExecState = "execution_unknown"
)

func (e ExecState) terminal() bool {
	switch e {
	case ExecCompleted, ExecFailed, ExecCancelled, ExecInterrupted:
		return true
	}
	return false
}

// live reports states that still occupy the task for overlap purposes.
func (e ExecState) live() bool {
	return e == ExecQueued || e == ExecReserved || e == ExecRunning || e == ExecCancelRequested || e == ExecExecutionUnknown
}

var runEdges = map[ExecState][]ExecState{
	ExecQueued:           {ExecRunning, ExecCancelled, ExecFailed, ExecCancelRequested, ExecExecutionUnknown},
	ExecRunning:          {ExecCompleted, ExecFailed, ExecCancelRequested, ExecExecutionUnknown, ExecInterrupted},
	ExecCancelRequested:  {ExecCancelled, ExecExecutionUnknown},
	ExecExecutionUnknown: {ExecCompleted, ExecFailed, ExecCancelled, ExecInterrupted},
}

var attemptEdges = map[ExecState][]ExecState{
	ExecQueued:           {ExecReserved, ExecCancelled, ExecFailed, ExecCancelRequested, ExecExecutionUnknown}, // unknown: recovery found contradictory evidence (a frozen result) for a never-reserved attempt
	ExecReserved:         {ExecRunning, ExecFailed, ExecCancelled, ExecCancelRequested, ExecExecutionUnknown},
	ExecRunning:          runEdges[ExecRunning],
	ExecCancelRequested:  runEdges[ExecCancelRequested],
	ExecExecutionUnknown: runEdges[ExecExecutionUnknown],
}

// CanRunTransition and CanAttemptTransition expose the typed transition graph.
func CanRunTransition(from, to ExecState) bool { return slices.Contains(runEdges[from], to) }

// CanAttemptTransition is CanRunTransition for attempts (adds the reserved step).
func CanAttemptTransition(from, to ExecState) bool { return slices.Contains(attemptEdges[from], to) }

var (
	ErrRunNotFound       = errors.New("store: task run not found")
	ErrAttemptNotFound   = errors.New("store: task attempt not found")
	ErrRunSettled        = errors.New("store: task run is already settled")
	ErrInvalidTransition = errors.New("store: invalid execution transition")
	// ErrTransitionConflict means the row was not in the expected "from" state.
	ErrTransitionConflict       = errors.New("store: execution state changed concurrently")
	ErrReservationNotReleasable = errors.New("store: attempt reservation cannot be released")
	ErrInvalidVerification      = errors.New("store: invalid verification receipt")
)

// TaskRun is one logical occurrence of a managed task (task_runs row).
type TaskRun struct {
	ID               string
	TaskID           string
	Revision         int64
	NominalAt        int64
	Conversation     ConversationScope
	ActorRef         string
	DeliveryMode     string
	State            ExecState
	PublicationState string
	ResultID         string
	GoalStatus       string
	GoalAuthority    string
	EffectsState     string
	StopRequestedAt  int64
	StopReason       string
	FailureCode      string
	CreatedAt        int64
	SettledAt        int64
}

// TaskAttempt is one execution attempt of a run with its own durable turn.
type TaskAttempt struct {
	ID                 string
	RunID              string
	Number             int
	TurnID             int64
	State              ExecState
	Provider           string
	Model              string
	ProfileFingerprint string
	NativeSessionUUID  string
	Reservation        string // none, held, released
	StartedAt          int64
	ExitedAt           int64
	ExitEvidence       string
	FailureCode        string
	CreatedAt          int64
}

const runColumns = `run_id, task_id, definition_revision, nominal_at, namespace_id, namespace_epoch, registration_revision, chat_jid, chat_folder,
 thread_id, visibility, actor_ref, delivery_mode, execution_state, publication_state, IFNULL(result_id,''), goal_status, goal_authority,
 effects_state, IFNULL(stop_requested_at,0), stop_reason, failure_code, created_at, IFNULL(settled_at,0)`

const attemptColumns = `attempt_id, run_id, attempt_no, turn_id, execution_state, provider, model, profile_fingerprint, native_session_uuid,
 reservation_state, IFNULL(started_at,0), IFNULL(exited_at,0), exit_evidence, failure_code, created_at`

func scanRun(row interface{ Scan(...any) error }) (TaskRun, error) {
	var r TaskRun
	var vis, state string
	sc := &r.Conversation.Scope
	err := row.Scan(&r.ID, &r.TaskID, &r.Revision, &r.NominalAt, &sc.NamespaceID, &sc.NamespaceEpoch, &sc.RegistrationRevision, &sc.JID, &sc.Folder,
		&r.Conversation.ThreadID, &vis, &r.ActorRef, &r.DeliveryMode, &state, &r.PublicationState, &r.ResultID, &r.GoalStatus, &r.GoalAuthority,
		&r.EffectsState, &r.StopRequestedAt, &r.StopReason, &r.FailureCode, &r.CreatedAt, &r.SettledAt)
	r.Conversation.Visibility, r.State = Visibility(vis), ExecState(state)
	return r, err
}

func scanAttempt(row interface{ Scan(...any) error }) (TaskAttempt, error) {
	var a TaskAttempt
	var state string
	err := row.Scan(&a.ID, &a.RunID, &a.Number, &a.TurnID, &state, &a.Provider, &a.Model, &a.ProfileFingerprint, &a.NativeSessionUUID,
		&a.Reservation, &a.StartedAt, &a.ExitedAt, &a.ExitEvidence, &a.FailureCode, &a.CreatedAt)
	a.State = ExecState(state)
	return a, err
}

func readRunQ(ctx context.Context, q taskSQL, id string) (TaskRun, error) {
	r, err := scanRun(q.QueryRowContext(ctx, `SELECT `+runColumns+` FROM task_runs WHERE run_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRun{}, ErrRunNotFound
	}
	return r, err
}

func attemptsForRunQ(ctx context.Context, q taskSQL, runID string) ([]TaskAttempt, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+attemptColumns+` FROM task_attempts WHERE run_id=? ORDER BY attempt_no`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskAttempt
	for rows.Next() {
		a, e := scanAttempt(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetTaskRunRecord returns one ledger run by ID. (GetTaskRun addresses the
// legacy task_run_logs compatibility rows.)
func (s *Store) GetTaskRunRecord(ctx context.Context, id string) (TaskRun, error) {
	return readRunQ(ctx, s.taskDB(ctx), id)
}

// GetTaskAttempt returns one attempt by ID.
func (s *Store) GetTaskAttempt(ctx context.Context, id string) (TaskAttempt, error) {
	a, err := scanAttempt(s.taskDB(ctx).QueryRowContext(ctx, `SELECT `+attemptColumns+` FROM task_attempts WHERE attempt_id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return TaskAttempt{}, ErrAttemptNotFound
	}
	return a, err
}

// TaskRunAttempts lists the attempts of a run in order.
func (s *Store) TaskRunAttempts(ctx context.Context, runID string) ([]TaskAttempt, error) {
	return attemptsForRunQ(ctx, s.taskDB(ctx), runID)
}

// ListTaskRunRecords returns the newest runs of a task first.
func (s *Store) ListTaskRunRecords(ctx context.Context, taskID string, limit int) ([]TaskRun, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT `+runColumns+` FROM task_runs WHERE task_id=? ORDER BY created_at DESC, run_id LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRun
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func liveRunsQ(ctx context.Context, q taskSQL, taskID string) ([]TaskRun, error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+` FROM task_runs WHERE task_id=? AND execution_state IN ('queued','running','cancel_requested','execution_unknown') ORDER BY created_at, run_id`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskRun
	for rows.Next() {
		r, e := scanRun(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// TransitionOptions carries the evidence of a state change. Only host code
// calls these methods; the model never writes execution state.
type TransitionOptions struct {
	FailureCode string // fixed machine code, optional
	// Evidence is the host observation behind the change: an observed local
	// exit when leaving a possibly started state, or operator reconciliation
	// when leaving execution_unknown. Never a model assertion.
	Evidence          string
	EffectsUnresolved bool // the run may have left external effects uncertain
	// Attempt only: provider snapshot (set once, when reserved) and the native
	// session UUID observed at start (diagnostic, not resume authority).
	Provider, Model, ProfileFingerprint string
	NativeSessionUUID                   string
}

func (o TransitionOptions) validate() error {
	if o.FailureCode != "" && !reasonPattern.MatchString(o.FailureCode) {
		return fmt.Errorf("%w: failure code must be a fixed machine code", ErrInvalidTransition)
	}
	if len(o.Evidence) > 400 {
		return fmt.Errorf("%w: evidence is bounded to 400 bytes", ErrInvalidTransition)
	}
	return nil
}

func needsEvidence(from, to ExecState) bool {
	return to.terminal() && (from == ExecRunning || from == ExecCancelRequested || from == ExecExecutionUnknown)
}

// TransitionRun moves a run along the typed graph with compare-and-set on the
// current state. A started run cannot settle without host evidence.
func (s *Store) TransitionRun(ctx context.Context, runID string, from, to ExecState, o TransitionOptions) error {
	if !CanRunTransition(from, to) {
		return fmt.Errorf("%w: run %s -> %s", ErrInvalidTransition, from, to)
	}
	if err := o.validate(); err != nil {
		return err
	}
	if needsEvidence(from, to) && o.Evidence == "" {
		return fmt.Errorf("%w: %s -> %s needs host evidence", ErrInvalidTransition, from, to)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		return transitionRunQ(ctx, q, runID, from, to, o, time.Now().UnixMilli())
	})
	if err != nil {
		return fmt.Errorf("store: TransitionRun: %w", err)
	}
	return nil
}

// transitionRunQ is the handle-explicit compare-and-set behind TransitionRun, so
// dispatch and settlement can move run and attempt in one transaction. Graph
// and evidence validation stay in the exported wrapper and in settleQ.
func transitionRunQ(ctx context.Context, q taskSQL, runID string, from, to ExecState, o TransitionOptions, now int64) error {
	unresolved := to == ExecExecutionUnknown || o.EffectsUnresolved
	res, e := q.ExecContext(ctx, `UPDATE task_runs SET execution_state=?, failure_code=CASE WHEN ?<>'' THEN ? ELSE failure_code END,
 effects_state=CASE WHEN ? THEN 'unresolved' ELSE effects_state END,
 settled_at=CASE WHEN ? THEN ? ELSE settled_at END
 WHERE run_id=? AND execution_state=?`,
		string(to), o.FailureCode, o.FailureCode, unresolved, to.terminal(), now, runID, string(from))
	if e != nil {
		return e
	}
	return transitionResult(ctx, q, res, "task_runs", "run_id", runID, ErrRunNotFound)
}

func transitionResult(ctx context.Context, q taskSQL, res sql.Result, table, key, id string, notFound error) error {
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	var one int
	err = q.QueryRowContext(ctx, `SELECT 1 FROM `+table+` WHERE `+key+`=?`, id).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return notFound
	}
	if err != nil {
		return err
	}
	return ErrTransitionConflict
}

// TransitionAttempt is TransitionRun for attempts. Reserving an attempt holds
// capacity and freezes the provider snapshot; observed exit evidence on a
// terminal transition releases the reservation in the same write.
func (s *Store) TransitionAttempt(ctx context.Context, attemptID string, from, to ExecState, o TransitionOptions) error {
	if !CanAttemptTransition(from, to) {
		return fmt.Errorf("%w: attempt %s -> %s", ErrInvalidTransition, from, to)
	}
	if err := o.validate(); err != nil {
		return err
	}
	if needsEvidence(from, to) && o.Evidence == "" {
		return fmt.Errorf("%w: %s -> %s needs host evidence", ErrInvalidTransition, from, to)
	}
	if to == ExecReserved && o.Provider == "" {
		return fmt.Errorf("%w: reserving an attempt needs its provider snapshot", ErrInvalidTransition)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		return transitionAttemptQ(ctx, q, attemptID, from, to, o, time.Now().UnixMilli())
	})
	if err != nil {
		return fmt.Errorf("store: TransitionAttempt: %w", err)
	}
	return nil
}

// transitionAttemptQ is the handle-explicit form of TransitionAttempt.
func transitionAttemptQ(ctx context.Context, q taskSQL, attemptID string, from, to ExecState, o TransitionOptions, now int64) error {
	res, e := q.ExecContext(ctx, `UPDATE task_attempts SET execution_state=?,
 failure_code=CASE WHEN ?<>'' THEN ? ELSE failure_code END,
 provider=CASE WHEN provider='' THEN ? ELSE provider END,
 model=CASE WHEN provider='' THEN ? ELSE model END,
 profile_fingerprint=CASE WHEN provider='' THEN ? ELSE profile_fingerprint END,
 native_session_uuid=CASE WHEN ?<>'' THEN ? ELSE native_session_uuid END,
 started_at=CASE WHEN ?='running' THEN ? ELSE started_at END,
 exited_at=CASE WHEN ? AND ?<>'' THEN ? ELSE exited_at END,
 exit_evidence=CASE WHEN ? AND ?<>'' THEN ? ELSE exit_evidence END,
 reservation_state=CASE WHEN ?='reserved' THEN 'held'
   WHEN ? AND ?<>'' AND reservation_state='held' THEN 'released' ELSE reservation_state END
 WHERE attempt_id=? AND execution_state=?`,
		string(to), o.FailureCode, o.FailureCode, o.Provider, o.Model, o.ProfileFingerprint,
		o.NativeSessionUUID, o.NativeSessionUUID, string(to), now,
		to.terminal(), o.Evidence, now, to.terminal(), o.Evidence, o.Evidence,
		string(to), to.terminal(), o.Evidence, attemptID, string(from))
	if e != nil {
		return e
	}
	return transitionResult(ctx, q, res, "task_attempts", "attempt_id", attemptID, ErrAttemptNotFound)
}

// ReleaseProof names why a held reservation may be released.
type ReleaseProof string

const (
	// ProofLocalExit: the host observed the owned local process exit. Remote
	// effects may still be uncertain and still forbid replay.
	ProofLocalExit ReleaseProof = "local_exit_observed"
	// ProofOperatorReconciled: an operator established the runtime is stopped.
	ProofOperatorReconciled ReleaseProof = "operator_reconciled"
)

// ReleaseAttemptReservation releases a held reservation once, with evidence,
// and only when the attempt can no longer be executing according to the
// ledger. A running or cancel-requested attempt is never releasable.
func (s *Store) ReleaseAttemptReservation(ctx context.Context, attemptID string, proof ReleaseProof, detail string) error {
	if (proof != ProofLocalExit && proof != ProofOperatorReconciled) || detail == "" || len(detail) > 350 {
		return fmt.Errorf("%w: proof and bounded detail required", ErrReservationNotReleasable)
	}
	now := time.Now().UnixMilli()
	_, err := s.withTx(ctx, func(q taskSQL) error {
		res, e := q.ExecContext(ctx, `UPDATE task_attempts SET reservation_state='released', exit_evidence=?, exited_at=COALESCE(exited_at,?)
 WHERE attempt_id=? AND reservation_state='held'
   AND execution_state IN ('execution_unknown','completed','failed','cancelled','interrupted')`,
			string(proof)+":"+detail, now, attemptID)
		if e != nil {
			return e
		}
		n, e := res.RowsAffected()
		if e != nil {
			return e
		}
		if n != 1 {
			return ErrReservationNotReleasable
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("store: ReleaseAttemptReservation: %w", err)
	}
	return nil
}

// ReconcileRunEffects records that an operator reconciled uncertain external
// effects. It does not change execution state and never authorizes replay.
func (s *Store) ReconcileRunEffects(ctx context.Context, runID, evidence string) error {
	if evidence == "" || len(evidence) > 400 {
		return fmt.Errorf("%w: reconciliation evidence required", ErrInvalidTransition)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		res, e := q.ExecContext(ctx, `UPDATE task_runs SET effects_state='reconciled', effects_evidence=? WHERE run_id=? AND effects_state='unresolved'`, evidence, runID)
		if e != nil {
			return e
		}
		return transitionResult(ctx, q, res, "task_runs", "run_id", runID, ErrRunNotFound)
	})
	if err != nil {
		return fmt.Errorf("store: ReconcileRunEffects: %w", err)
	}
	return nil
}

// RunStopResult reports the requested versus settled outcome of a stop-run.
type RunStopResult struct {
	RunID string
	State ExecState
	// Settled is true only when the run was proven never started and is now
	// cancelled. Otherwise cancellation is merely requested.
	Settled bool
}

// RequestRunStop is the execution intent "stop this run". It is scope- and
// run-bound and distinct from pausing or tombstoning the schedule, which only
// prevent future admission. A run proven never started is cancelled at once;
// anything possibly started becomes cancel_requested (or stays
// execution_unknown) and keeps its reservation until real exit evidence.
func (s *Store) RequestRunStop(ctx context.Context, folder, taskID, runID, reason string) (RunStopResult, error) {
	if !reasonPattern.MatchString(reason) {
		return RunStopResult{}, fmt.Errorf("%w: stop reason must be a fixed machine code", ErrInvalidTransition)
	}
	var out RunStopResult
	_, err := s.withTx(ctx, func(q taskSQL) error {
		r, e := readRunQ(ctx, q, runID)
		if e != nil {
			return e
		}
		if r.TaskID != taskID || r.Conversation.Scope.Folder != folder {
			return ErrRunNotFound
		}
		out, e = requestRunStopQ(ctx, q, r, reason, time.Now().UnixMilli())
		return e
	})
	if err != nil {
		return RunStopResult{}, fmt.Errorf("store: RequestRunStop: %w", err)
	}
	return out, nil
}

func requestRunStopQ(ctx context.Context, q taskSQL, r TaskRun, reason string, now int64) (RunStopResult, error) {
	if r.State.terminal() {
		return RunStopResult{}, ErrRunSettled
	}
	attempts, err := attemptsForRunQ(ctx, q, r.ID)
	if err != nil {
		return RunStopResult{}, err
	}
	if r.State == ExecQueued {
		all := len(attempts) > 0
		for _, a := range attempts {
			ok, e := cancelUnstartedAttemptQ(ctx, q, a, now)
			if e != nil {
				return RunStopResult{}, e
			}
			all = all && ok
		}
		if all {
			if _, err = q.ExecContext(ctx, `UPDATE task_runs SET execution_state='cancelled', stop_requested_at=COALESCE(stop_requested_at,?), stop_reason=?, failure_code='cancelled_before_start', settled_at=? WHERE run_id=? AND execution_state='queued'`,
				now, reason, now, r.ID); err != nil {
				return RunStopResult{}, err
			}
			return RunStopResult{RunID: r.ID, State: ExecCancelled, Settled: true}, nil
		}
	}
	// Possibly started: request, never claim settled.
	next := r.State
	if r.State == ExecQueued || r.State == ExecRunning {
		next = ExecCancelRequested
	}
	if _, err = q.ExecContext(ctx, `UPDATE task_runs SET execution_state=?, stop_requested_at=COALESCE(stop_requested_at,?), stop_reason=CASE WHEN stop_reason='' THEN ? ELSE stop_reason END WHERE run_id=?`,
		string(next), now, reason, r.ID); err != nil {
		return RunStopResult{}, err
	}
	if _, err = q.ExecContext(ctx, `UPDATE task_attempts SET execution_state='cancel_requested' WHERE run_id=? AND execution_state IN ('queued','reserved','running')`, r.ID); err != nil {
		return RunStopResult{}, err
	}
	return RunStopResult{RunID: r.ID, State: next}, nil
}

// cancelUnstartedAttemptQ cancels an attempt only when the durable turn is
// still pending with zero attempts: the database, not provider prose, is the
// proof that nothing started. A reserved attempt never qualifies.
func cancelUnstartedAttemptQ(ctx context.Context, q taskSQL, a TaskAttempt, now int64) (bool, error) {
	if a.State != ExecQueued || a.Reservation != "none" {
		return false, nil
	}
	res, err := q.ExecContext(ctx, `UPDATE turns SET status=?, error='task_run_cancelled', finished_at=? WHERE id=? AND status=? AND attempts=0`,
		TurnStopped, now, a.TurnID, TurnPending)
	if err != nil {
		return false, err
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return false, e
	}
	res, err = q.ExecContext(ctx, `UPDATE task_attempts SET execution_state='cancelled', failure_code='cancelled_before_start' WHERE attempt_id=? AND execution_state='queued'`, a.ID)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

// VerificationOutcome is the narrow result of a host check.
type VerificationOutcome string

const (
	VerifyPassed       VerificationOutcome = "passed"
	VerifyFailed       VerificationOutcome = "failed"
	VerifyInconclusive VerificationOutcome = "inconclusive"
)

// TaskVerification is a host-generated receipt naming what was actually
// checked (for example artifact integrity). It is not a claim that the goal
// is true and no agent-facing API writes it.
type TaskVerification struct {
	ReceiptID      string
	RunID          string
	AttemptID      string // optional
	ResultID       string // optional
	CheckerName    string
	CheckerVersion string
	CheckedDigest  string // lowercase hex SHA-256 of the checked input, optional
	Outcome        VerificationOutcome
	CheckedAt      int64
}

// RecordTaskVerification stores an immutable host verification receipt. The
// attempt and result, when given, must belong to the run.
func (s *Store) RecordTaskVerification(ctx context.Context, v TaskVerification) (TaskVerification, error) {
	switch v.Outcome {
	case VerifyPassed, VerifyFailed, VerifyInconclusive:
	default:
		return TaskVerification{}, fmt.Errorf("%w: outcome %q", ErrInvalidVerification, v.Outcome)
	}
	if v.RunID == "" || !checkerPattern.MatchString(v.CheckerName) || !checkerPattern.MatchString(v.CheckerVersion) ||
		(v.CheckedDigest != "" && !digestPattern.MatchString(v.CheckedDigest)) {
		return TaskVerification{}, fmt.Errorf("%w: checker identity or digest", ErrInvalidVerification)
	}
	id, err := newUUID()
	if err != nil {
		return TaskVerification{}, err
	}
	v.ReceiptID = id
	if v.CheckedAt == 0 {
		v.CheckedAt = time.Now().UnixMilli()
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if _, e := readRunQ(ctx, q, v.RunID); e != nil {
			return e
		}
		if v.AttemptID != "" {
			var n int
			if e := q.QueryRowContext(ctx, `SELECT count(*) FROM task_attempts WHERE attempt_id=? AND run_id=?`, v.AttemptID, v.RunID).Scan(&n); e != nil {
				return e
			}
			if n != 1 {
				return fmt.Errorf("%w: attempt belongs to another run", ErrInvalidVerification)
			}
		}
		if v.ResultID != "" {
			var n int
			if e := q.QueryRowContext(ctx, `SELECT count(*) FROM turn_result_members m JOIN task_attempts a ON a.turn_id=m.turn_id WHERE m.result_id=? AND a.run_id=?`, v.ResultID, v.RunID).Scan(&n); e != nil {
				return e
			}
			if n == 0 {
				return fmt.Errorf("%w: result does not belong to the run", ErrInvalidVerification)
			}
		}
		_, e := q.ExecContext(ctx, `INSERT INTO task_verifications(receipt_id,run_id,attempt_id,result_id,checker_name,checker_version,checked_digest,check_outcome,checked_at) VALUES(?,?,?,?,?,?,?,?,?)`,
			v.ReceiptID, v.RunID, nullableString(v.AttemptID), nullableString(v.ResultID), v.CheckerName, v.CheckerVersion, v.CheckedDigest, string(v.Outcome), v.CheckedAt)
		return e
	})
	if err != nil {
		return TaskVerification{}, fmt.Errorf("store: RecordTaskVerification: %w", err)
	}
	return v, nil
}

// PurgeBlocker names one reason managed evidence cannot be purged.
type PurgeBlocker string

const (
	BlockDefinitionActive     PurgeBlocker = "definition_not_tombstoned"
	BlockRunUnsettled         PurgeBlocker = "run_unsettled"
	BlockReservationHeld      PurgeBlocker = "reservation_held"
	BlockEffectsUnresolved    PurgeBlocker = "effects_unresolved"
	BlockPublicationPending   PurgeBlocker = "publication_pending"
	BlockDeliveryUnresolved   PurgeBlocker = "delivery_unresolved"
	BlockAuditExportUnchecked PurgeBlocker = "audit_export_unverified"
)

// ErrPurgeBlocked is returned by RequireTaskPurgeable.
var ErrPurgeBlocked = errors.New("store: managed task evidence cannot be purged")

// TaskPurgeBlockers lists every reason the evidence of a managed task must be
// kept. No audit export mechanism exists in this slice, so
// BlockAuditExportUnchecked is always present and nothing is purgeable; the
// other entries explain why even a verified export would not be enough.
func (s *Store) TaskPurgeBlockers(ctx context.Context, taskID string) ([]PurgeBlocker, error) {
	q := s.taskDB(ctx)
	t, err := readManagedTaskQ(ctx, q, taskID)
	if err != nil {
		return nil, err
	}
	var out []PurgeBlocker
	if !t.Tombstoned() {
		out = append(out, BlockDefinitionActive)
	}
	counts := []struct {
		blocker PurgeBlocker
		query   string
		args    []any
	}{
		{BlockRunUnsettled, `SELECT count(*) FROM task_runs WHERE task_id=? AND execution_state IN ('queued','running','cancel_requested','execution_unknown')`, []any{taskID}},
		{BlockReservationHeld, `SELECT count(*) FROM task_attempts a JOIN task_runs r ON r.run_id=a.run_id WHERE r.task_id=? AND a.reservation_state='held'`, []any{taskID}},
		{BlockEffectsUnresolved, `SELECT count(*) FROM task_runs WHERE task_id=? AND effects_state='unresolved'`, []any{taskID}},
		{BlockPublicationPending, `SELECT count(*) FROM task_runs WHERE task_id=? AND publication_state='pending'`, []any{taskID}},
		{BlockDeliveryUnresolved, `SELECT count(*) FROM delivery_groups WHERE unresolved=1 AND (task_ref=? OR delivery_id IN (SELECT delivery_id FROM task_occurrences WHERE task_id=? AND delivery_id IS NOT NULL))`, []any{taskID, taskID}},
	}
	for _, c := range counts {
		var n int
		if err = q.QueryRowContext(ctx, c.query, c.args...).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			out = append(out, c.blocker)
		}
	}
	return append(out, BlockAuditExportUnchecked), nil
}

// RequireTaskPurgeable returns ErrPurgeBlocked listing the blockers.
func (s *Store) RequireTaskPurgeable(ctx context.Context, taskID string) error {
	blockers, err := s.TaskPurgeBlockers(ctx, taskID)
	if err != nil {
		return err
	}
	if len(blockers) > 0 {
		return fmt.Errorf("%w: %v", ErrPurgeBlocked, blockers)
	}
	return nil
}
