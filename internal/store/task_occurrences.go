package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
)

// OccurrenceDecision is the recorded fate of one nominal slot.
type OccurrenceDecision string

const (
	DecisionAdmitted  OccurrenceDecision = "admitted"
	DecisionSkipped   OccurrenceDecision = "skipped"
	DecisionCoalesced OccurrenceDecision = "coalesced"
	DecisionExpired   OccurrenceDecision = "expired"
)

// MaxOccurrenceDecisions bounds the skipped/coalesced/expired rows recorded by
// one admission (CP-022: at most 100 candidates per task per tick; the rest is
// summarized by the planner, never fabricated as thousands of rows).
const MaxOccurrenceDecisions = 100

var (
	ErrInvalidAdmission    = errors.New("store: invalid occurrence admission")
	ErrCursorConflict      = errors.New("store: task cursor changed or was rewound")
	ErrTaskNotActive       = errors.New("store: task is not active")
	ErrTaskExpired         = errors.New("store: task definition has expired")
	ErrOccurrenceExpired   = errors.New("store: occurrence is past its expiry")
	ErrMaxRunsReached      = errors.New("store: task reached max_runs")
	ErrTaskOccupied        = errors.New("store: task already has live work under its overlap policy")
	ErrQueueFull           = errors.New("store: task queue limit reached")
	ErrOccurrenceDuplicate = errors.New("store: occurrence already recorded for this revision and nominal time")
)

// SlotDecision records a non-admitted nominal slot with a fixed reason code.
type SlotDecision struct {
	NominalAt int64
	Decision  OccurrenceDecision // skipped, coalesced or expired
	Reason    string
}

// AdmissionRequest is a planned, bounded admission. The planner (cron/DST/
// policy code outside the store) decides which slot to admit and where the
// cursor goes; the store makes that decision atomic and enforces the safety
// invariants (revision/cursor compare-and-set, scope, overlap, expiry, max
// runs, uniqueness, monotone cursor).
type AdmissionRequest struct {
	TaskID           string
	ExpectedRevision int64
	ExpectedNextRun  int64
	Now              int64 // unix ms; injected so tests use a fake clock

	// NominalAt is the slot to admit (0 = admit nothing, record decisions only).
	NominalAt   int64
	WallSlotKey string // optional local wall-slot dedupe key (DST fold "once")
	// SupersedeQueued cancels proven-never-started queued runs of the task so a
	// coalesced continuation replaces them; their durable turns are stopped.
	SupersedeQueued bool
	Decisions       []SlotDecision

	// NextRun is the new cursor: 0 or strictly after ExpectedNextRun. NewStatus
	// is "" / "active" (keep running), "paused" or "done"; the last two need a
	// TerminalReason. A task that is done or paused is never "achieved".
	NextRun        int64
	NewStatus      string
	TerminalReason string
}

// TaskOccurrence is one recorded decision row.
type TaskOccurrence struct {
	ID          string
	TaskID      string
	Revision    int64
	NominalAt   int64
	WallSlotKey string
	Decision    OccurrenceDecision
	Reason      string
	RunID       string
	DeliveryID  string
	CreatedAt   int64
}

// Admission is the committed result. TurnID or DeliveryID is what the caller
// schedules/dispatches after commit; both are zero when nothing was admitted.
type Admission struct {
	Occurrence TaskOccurrence // the admitted occurrence, zero value if none
	RunID      string
	AttemptID  string
	TurnID     int64
	DeliveryID string
	Task       ManagedTask // definition after the cursor advance
}

type admitFailpointKey struct{}

// admitFailpoint lets tests fail a named stage of AdmitTaskOccurrence. It is a
// context value so production code paths carry no mutable global.
func admitFailpoint(ctx context.Context, stage string) error {
	if f, ok := ctx.Value(admitFailpointKey{}).(func(string) error); ok {
		return f(stage)
	}
	return nil
}

func (r AdmissionRequest) validate() error {
	bad := func(format string, args ...any) error {
		return fmt.Errorf("%w: %s", ErrInvalidAdmission, fmt.Sprintf(format, args...))
	}
	switch {
	case r.TaskID == "" || r.ExpectedRevision < 1 || r.ExpectedNextRun <= 0 || r.Now <= 0:
		return bad("task, revision, cursor and clock are required")
	case r.NominalAt < 0 || r.NextRun < 0:
		return bad("negative time")
	case len(r.Decisions) > MaxOccurrenceDecisions:
		return bad("%d decisions (max %d)", len(r.Decisions), MaxOccurrenceDecisions)
	case r.NominalAt > r.Now:
		return bad("nominal time is in the future")
	}
	switch r.NewStatus {
	case "", "active":
		if r.NextRun <= r.ExpectedNextRun {
			return fmt.Errorf("%w: next cursor must move forward", ErrCursorConflict)
		}
		if r.TerminalReason != "" {
			return bad("terminal reason on an active task")
		}
	case "paused", "done":
		if !reasonPattern.MatchString(r.TerminalReason) {
			return bad("%s needs a fixed terminal reason", r.NewStatus)
		}
		if r.NextRun != 0 && r.NextRun <= r.ExpectedNextRun {
			return fmt.Errorf("%w: next cursor must move forward", ErrCursorConflict)
		}
		if r.NewStatus == "done" && r.NextRun != 0 {
			return bad("a finished task has no cursor")
		}
	default:
		return bad("status %q", r.NewStatus)
	}
	if r.NominalAt == 0 && len(r.Decisions) == 0 && r.NewStatus != "paused" && r.NewStatus != "done" {
		return bad("moving the cursor needs an admission or a recorded decision")
	}
	seen := map[int64]bool{}
	if r.NominalAt != 0 {
		seen[r.NominalAt] = true
	}
	for _, d := range r.Decisions {
		switch d.Decision {
		case DecisionSkipped, DecisionCoalesced, DecisionExpired:
		default:
			return bad("decision %q", d.Decision)
		}
		if d.NominalAt <= 0 || seen[d.NominalAt] || !reasonPattern.MatchString(d.Reason) {
			return bad("decision slot %d is invalid or repeated", d.NominalAt)
		}
		seen[d.NominalAt] = true
	}
	return nil
}

// AdmitTaskOccurrence is the single transactional admission of a nominal slot.
// In one SQLite transaction it reloads the definition, compare-and-sets
// revision and cursor, revalidates the stored ScopeRef, applies expiry,
// max_runs and overlap rules, records the bounded decisions, creates the run,
// attempt and durable turn (agent) or the CP-002 outbox delivery (reminder),
// and advances the cursor. Any failure rolls back every row, so there is never
// a partial cursor advance and, between competing callers, exactly one wins.
//
// Nothing here touches Queue, Runner, the network or the outer DB handle: all
// writes use the transaction handle, and the caller schedules the returned
// turn (or notifies the dispatcher, done here for a standalone commit) only
// after commit. Hold LockTasks outside for in-process ordering with commands;
// the SQL compare-and-set, not that mutex, is the safety boundary.
func (s *Store) AdmitTaskOccurrence(ctx context.Context, req AdmissionRequest) (Admission, error) {
	if err := req.validate(); err != nil {
		return Admission{}, err
	}
	var out Admission
	enlisted, err := s.withTx(ctx, func(q taskSQL) error {
		var e error
		out, e = s.admitQ(ctx, q, req)
		return e
	})
	if err != nil {
		return Admission{}, fmt.Errorf("store: AdmitTaskOccurrence: %w", err)
	}
	if out.DeliveryID != "" && !enlisted {
		s.NotifyPublished(out.DeliveryID)
	}
	return out, nil
}

func (s *Store) admitQ(ctx context.Context, q taskSQL, req AdmissionRequest) (Admission, error) {
	t, err := readManagedTaskQ(ctx, q, req.TaskID)
	if err != nil {
		return Admission{}, err
	}
	switch {
	case t.Tombstoned():
		return Admission{}, ErrTaskTombstoned
	case t.Revision != req.ExpectedRevision:
		return Admission{}, ErrRevisionConflict
	case t.Status != "active":
		return Admission{}, ErrTaskNotActive
	case t.NextRun != req.ExpectedNextRun:
		return Admission{}, ErrCursorConflict
	}
	sc := t.Conversation.Scope
	if err = validateScopeQ(ctx, q, sc); err != nil {
		return Admission{}, err
	}
	if req.NominalAt != 0 {
		if t.ExpiresAt != 0 && req.NominalAt >= t.ExpiresAt {
			return Admission{}, ErrTaskExpired
		}
		if t.MaxRuns != 0 {
			var n int64
			if err = q.QueryRowContext(ctx, `SELECT count(*) FROM task_occurrences WHERE task_id=? AND decision='admitted'`, t.ID).Scan(&n); err != nil {
				return Admission{}, err
			}
			if n >= t.MaxRuns {
				return Admission{}, ErrMaxRunsReached
			}
		}
	}

	now := req.Now // the injected clock is the only clock inside admission
	slots := make([]int64, 0, len(req.Decisions)+1)
	for _, d := range req.Decisions {
		slots = append(slots, d.NominalAt)
	}
	if req.NominalAt != 0 {
		slots = append(slots, req.NominalAt)
	}
	for _, at := range slots {
		var n int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM task_occurrences WHERE task_id=? AND definition_revision=? AND nominal_at=?`, t.ID, t.Revision, at).Scan(&n); err != nil {
			return Admission{}, err
		}
		if n != 0 {
			return Admission{}, ErrOccurrenceDuplicate
		}
	}
	if req.NominalAt != 0 && req.WallSlotKey != "" {
		var n int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM task_occurrences WHERE task_id=? AND definition_revision=? AND wall_slot_key=?`, t.ID, t.Revision, req.WallSlotKey).Scan(&n); err != nil {
			return Admission{}, err
		}
		if n != 0 {
			return Admission{}, ErrOccurrenceDuplicate
		}
	}
	for i, d := range req.Decisions {
		id, e := newUUID()
		if e != nil {
			return Admission{}, e
		}
		if err = insertOccurrenceQ(ctx, q, TaskOccurrence{ID: id, TaskID: t.ID, Revision: t.Revision, NominalAt: d.NominalAt, Decision: d.Decision, Reason: d.Reason, CreatedAt: now}, t); err != nil {
			return Admission{}, err
		}
		if err = admitFailpoint(ctx, fmt.Sprintf("decision:%d", i)); err != nil {
			return Admission{}, err
		}
	}
	if err = admitFailpoint(ctx, "decisions"); err != nil {
		return Admission{}, err
	}

	var out Admission
	if req.NominalAt != 0 {
		switch t.Kind {
		case TaskKindAgent:
			out, err = s.admitAgentQ(ctx, q, t, req, now)
		case TaskKindReminder:
			out, err = s.admitReminderQ(ctx, q, t, req, now)
		}
		if err != nil {
			return Admission{}, err
		}
	}

	status := req.NewStatus
	if status == "" {
		status = "active"
	}
	lastRun := t.LastRun
	if req.NominalAt != 0 {
		lastRun = req.Now
	}
	res, err := q.ExecContext(ctx, `UPDATE scheduled_tasks SET next_run=?, last_run=?, status=?, terminal_reason=?
 WHERE id=? AND definition_revision=? AND next_run=? AND status='active' AND deleted_at IS NULL`,
		req.NextRun, nullableInt64(lastRun), status, req.TerminalReason, t.ID, t.Revision, req.ExpectedNextRun)
	if err != nil {
		return Admission{}, err
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return Admission{}, errors.Join(ErrCursorConflict, e)
	}
	if err = admitFailpoint(ctx, "cursor"); err != nil {
		return Admission{}, err
	}
	t.NextRun, t.LastRun, t.Status, t.TerminalReason = req.NextRun, lastRun, status, req.TerminalReason
	out.Task = t
	return out, nil
}

func insertOccurrenceQ(ctx context.Context, q taskSQL, o TaskOccurrence, t ManagedTask) error {
	sc := t.Conversation.Scope
	_, err := q.ExecContext(ctx, `INSERT INTO task_occurrences(occurrence_id,task_id,definition_revision,nominal_at,wall_slot_key,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,decision,reason,run_id,delivery_id,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.ID, o.TaskID, o.Revision, o.NominalAt, o.WallSlotKey, sc.NamespaceID, sc.NamespaceEpoch, sc.RegistrationRevision, sc.JID, sc.Folder,
		t.Conversation.ThreadID, string(t.Conversation.Visibility), string(o.Decision), o.Reason, nullableString(o.RunID), nullableString(o.DeliveryID), o.CreatedAt)
	if err != nil {
		return fmt.Errorf("store: record occurrence %d: %w", o.NominalAt, err)
	}
	return nil
}

// occupancyQ classifies the task's non-terminal work for overlap decisions.
// Queued means not yet reserved; anything else live (running, cancel_requested,
// execution_unknown, or a held reservation even on a settled run) is busy.
func occupancyQ(ctx context.Context, q taskSQL, taskID string) (queued []TaskRun, busy int, err error) {
	rows, err := q.QueryContext(ctx, `SELECT `+runColumns+`, (SELECT count(*) FROM task_attempts a WHERE a.run_id=task_runs.run_id AND (a.reservation_state='held' OR a.execution_state IN ('reserved','running','cancel_requested','execution_unknown')))
 FROM task_runs WHERE task_id=? AND (execution_state IN ('queued','running','cancel_requested','execution_unknown')
  OR EXISTS(SELECT 1 FROM task_attempts a WHERE a.run_id=task_runs.run_id AND a.reservation_state='held'))`, taskID)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	for rows.Next() {
		var r TaskRun
		var vis, state string
		var active int
		sc := &r.Conversation.Scope
		if err = rows.Scan(&r.ID, &r.TaskID, &r.Revision, &r.NominalAt, &sc.NamespaceID, &sc.NamespaceEpoch, &sc.RegistrationRevision, &sc.JID, &sc.Folder,
			&r.Conversation.ThreadID, &vis, &r.ActorRef, &r.DeliveryMode, &state, &r.PublicationState, &r.ResultID, &r.GoalStatus, &r.GoalAuthority,
			&r.EffectsState, &r.StopRequestedAt, &r.StopReason, &r.FailureCode, &r.CreatedAt, &r.SettledAt, &active); err != nil {
			return nil, 0, err
		}
		r.State = ExecState(state)
		if r.State == ExecQueued && active == 0 {
			queued = append(queued, r)
		} else {
			busy++
		}
	}
	return queued, busy, rows.Err()
}

func (s *Store) admitAgentQ(ctx context.Context, q taskSQL, t ManagedTask, req AdmissionRequest, now int64) (Admission, error) {
	queued, busy, err := occupancyQ(ctx, q, t.ID)
	if err != nil {
		return Admission{}, err
	}
	switch t.Overlap {
	case OverlapSkip:
		if len(queued)+busy > 0 {
			return Admission{}, ErrTaskOccupied
		}
	case OverlapCoalesce:
		if len(queued) > 0 && !req.SupersedeQueued {
			return Admission{}, ErrTaskOccupied
		}
	case OverlapQueue:
		if len(queued) >= t.QueueLimit {
			return Admission{}, ErrQueueFull
		}
	}
	if req.SupersedeQueued && t.Overlap == OverlapCoalesce {
		for _, r := range queued {
			attempts, e := attemptsForRunQ(ctx, q, r.ID)
			if e != nil {
				return Admission{}, e
			}
			for _, a := range attempts {
				ok, e := cancelUnstartedAttemptQ(ctx, q, a, now)
				if e != nil {
					return Admission{}, e
				}
				if !ok {
					return Admission{}, ErrTaskOccupied // started meanwhile: never replaced
				}
			}
			if _, e = q.ExecContext(ctx, `UPDATE task_runs SET execution_state='cancelled', stop_reason='superseded', failure_code='cancelled_before_start', settled_at=? WHERE run_id=? AND execution_state='queued'`, now, r.ID); e != nil {
				return Admission{}, e
			}
		}
	}

	runID, err := newUUID()
	if err != nil {
		return Admission{}, err
	}
	attemptID, err := newUUID()
	if err != nil {
		return Admission{}, err
	}
	occID, err := newUUID()
	if err != nil {
		return Admission{}, err
	}
	sc := t.Conversation.Scope
	if _, err = q.ExecContext(ctx, `INSERT INTO task_runs(run_id,task_id,definition_revision,nominal_at,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,actor_ref,trigger_kind,session_kind,delivery_mode,execution_state,created_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,'schedule','isolated',?,'queued',?)`,
		runID, t.ID, t.Revision, req.NominalAt, sc.NamespaceID, sc.NamespaceEpoch, sc.RegistrationRevision, sc.JID, sc.Folder,
		t.Conversation.ThreadID, string(t.Conversation.Visibility), t.ActorRef, t.DeliveryMode, now); err != nil {
		return Admission{}, fmt.Errorf("store: insert run: %w", err)
	}
	if err = admitFailpoint(ctx, "run"); err != nil {
		return Admission{}, err
	}
	// The durable turn is inserted on this transaction's handle, never through
	// Queue.Enqueue. The logical context key is not a Telegram thread.
	turnID, dup, err := insertTurnQ(ctx, q, Turn{
		Source: "task", SourceID: fmt.Sprintf("task:%s/run:%s/attempt:%s", t.ID, runID, attemptID),
		ChatFolder: sc.Folder, ThreadID: t.Conversation.ThreadID, IsOwner: false, Text: t.Prompt, Status: TurnPending, CreatedAt: now,
	})
	if err != nil {
		return Admission{}, err
	}
	if dup {
		return Admission{}, ErrOccurrenceDuplicate
	}
	if err = admitFailpoint(ctx, "turn"); err != nil {
		return Admission{}, err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO task_attempts(attempt_id,run_id,attempt_no,turn_id,execution_state,created_at) VALUES(?,?,1,?,'queued',?)`,
		attemptID, runID, turnID, now); err != nil {
		return Admission{}, fmt.Errorf("store: insert attempt: %w", err)
	}
	if err = admitFailpoint(ctx, "attempt"); err != nil {
		return Admission{}, err
	}
	occ := TaskOccurrence{ID: occID, TaskID: t.ID, Revision: t.Revision, NominalAt: req.NominalAt, WallSlotKey: req.WallSlotKey, Decision: DecisionAdmitted, Reason: "admitted", RunID: runID, CreatedAt: now}
	if err = insertOccurrenceQ(ctx, q, occ, t); err != nil {
		return Admission{}, err
	}
	if err = admitFailpoint(ctx, "occurrence"); err != nil {
		return Admission{}, err
	}
	return Admission{Occurrence: occ, RunID: runID, AttemptID: attemptID, TurnID: turnID}, nil
}

// admitReminderQ binds the exact preauthorized literal text and destination to
// a CP-002 outbox delivery in this transaction. No run, turn, Runner or model
// is involved. The request identity makes a repeat of the same slot converge
// on the same delivery even if the occurrence uniqueness were bypassed.
func (s *Store) admitReminderQ(ctx context.Context, q taskSQL, t ManagedTask, req AdmissionRequest, now int64) (Admission, error) {
	expires := req.NominalAt + ReminderDefaultExpiry.Milliseconds()
	if expires <= now {
		return Admission{}, ErrOccurrenceExpired
	}
	occID, err := newUUID()
	if err != nil {
		return Admission{}, err
	}
	sum := sha256.Sum256([]byte(t.LiteralText))
	pub := PublishRequest{
		Conversation: t.Conversation,
		Owner:        OwnerControllerResult,
		ActorRef:     t.ActorRef,
		TaskRef:      t.ID,
		RunRef:       occID,
		Parts:        []DeliveryPart{{Kind: PartText, Payload: []byte(t.LiteralText)}},
		ExpiresAt:    expires,
		Request:      &RequestIdentity{Key: fmt.Sprintf("task-reminder:%s:%d:%d", t.ID, t.Revision, req.NominalAt), PayloadHash: hex.EncodeToString(sum[:])},
	}
	if err = validatePublish(pub, now); err != nil {
		return Admission{}, err
	}
	d, err := publishDeliveryQ(ctx, q, s.outboxLimits(), pub, now)
	if err != nil {
		return Admission{}, err
	}
	if !d.Created {
		return Admission{}, ErrOccurrenceDuplicate
	}
	if err = admitFailpoint(ctx, "outbox"); err != nil {
		return Admission{}, err
	}
	occ := TaskOccurrence{ID: occID, TaskID: t.ID, Revision: t.Revision, NominalAt: req.NominalAt, WallSlotKey: req.WallSlotKey, Decision: DecisionAdmitted, Reason: "admitted", DeliveryID: d.ID, CreatedAt: now}
	if err = insertOccurrenceQ(ctx, q, occ, t); err != nil {
		return Admission{}, err
	}
	if err = admitFailpoint(ctx, "occurrence"); err != nil {
		return Admission{}, err
	}
	return Admission{Occurrence: occ, DeliveryID: d.ID}, nil
}

// ListTaskOccurrences returns recorded decisions newest nominal time first.
func (s *Store) ListTaskOccurrences(ctx context.Context, taskID string, limit int) ([]TaskOccurrence, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT occurrence_id,task_id,definition_revision,nominal_at,wall_slot_key,decision,reason,IFNULL(run_id,''),IFNULL(delivery_id,''),created_at
 FROM task_occurrences WHERE task_id=? ORDER BY nominal_at DESC, occurrence_id LIMIT ?`, taskID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TaskOccurrence
	for rows.Next() {
		var o TaskOccurrence
		var d string
		if err = rows.Scan(&o.ID, &o.TaskID, &o.Revision, &o.NominalAt, &o.WallSlotKey, &d, &o.Reason, &o.RunID, &o.DeliveryID, &o.CreatedAt); err != nil {
			return nil, err
		}
		o.Decision = OccurrenceDecision(d)
		out = append(out, o)
	}
	return out, rows.Err()
}
