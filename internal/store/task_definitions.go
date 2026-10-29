package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

// TaskKind separates model jobs from model-free literal reminders.
type TaskKind string

const (
	TaskKindAgent    TaskKind = "agent"
	TaskKindReminder TaskKind = "reminder"
)

// Session and delivery modes of a definition. Reminders use SessionNone: they
// allocate no native session and invoke no provider.
const (
	SessionLegacyMain = "legacy_main"
	SessionIsolated   = "isolated"
	SessionNone       = "none"

	TaskDeliveryLegacyIPC        = "legacy_ipc"
	TaskDeliveryControllerResult = "controller_result"
)

// Canonical policy values and bounds (CP-022).
const (
	OverlapSkip     = "skip"
	OverlapCoalesce = "coalesce"
	OverlapQueue    = "queue"

	MisfireSkip     = "skip"
	MisfireCoalesce = "coalesce"
	MisfireCatchUp  = "catch_up"

	DSTFoldOnce = "once"
	DSTFoldBoth = "both"

	DefaultMisfireGraceMs = 120_000
	MaxMisfireGraceMs     = int64(24 * time.Hour / time.Millisecond)
	DefaultQueueLimit     = 1
	DefaultCatchUpLimit   = 1
	MaxQueueLimit         = 10
	MaxCatchUpLimit       = 10

	MaxReminderTextBytes = 3500
	MaxTaskPromptBytes   = 64 << 10
	MaxTaskTextBytes     = 2048 // objective, done condition
	MaxActorRefBytes     = 255

	// ReminderDefaultExpiry is the delivery expiry after the nominal instant.
	ReminderDefaultExpiry = 24 * time.Hour
)

var (
	ErrInvalidTask       = errors.New("store: invalid task definition")
	ErrNotManagedTask    = errors.New("store: task is not a managed definition")
	ErrManagedTask       = errors.New("store: managed task must be changed through its revisioned API")
	ErrTaskTombstoned    = errors.New("store: task is deleted (tombstoned)")
	ErrRevisionConflict  = errors.New("store: task definition revision changed")
	ErrGroupTaskDisabled = errors.New("store: managed tasks are disabled for group chats")
	// ErrTaskHasManagedEvidence is returned when a legacy physical deletion
	// would orphan managed ledger rows.
	ErrTaskHasManagedEvidence = errors.New("store: task is referenced by managed ledger rows")
	// ErrScopeIdentityChanged is returned by a rebind that would move the task
	// to a different namespace, epoch, chat or folder.
	ErrScopeIdentityChanged = errors.New("store: task scope identity cannot change")
)

// ManagedTask is an execution_version 1 definition. Legacy rows stay
// TaskRecord; the two views never share mutators.
type ManagedTask struct {
	ID           string
	Kind         TaskKind
	Conversation ConversationScope // ScopeRef snapshot taken at creation / last rebind
	ActorRef     string            // authorized actor or service principal, host-derived
	Prompt       string            // agent kind only
	LiteralText  string            // reminder kind only
	SessionMode  string
	DeliveryMode string

	ScheduleType  string // cron, interval, once
	ScheduleValue string
	Timezone      string // explicit IANA zone
	NextRun       int64  // nominal cursor, unix ms; 0 = none
	LastRun       int64
	Status        string // active, paused, done

	Overlap        string
	Misfire        string
	MisfireGraceMs int64
	QueueLimit     int
	CatchUpLimit   int
	DSTFold        string
	ExpiresAt      int64 // 0 = none
	MaxRuns        int64 // 0 = unlimited
	Objective      string
	DoneCondition  string

	Revision       int64
	TerminalReason string
	CreatedAt      int64
	DeletedAt      int64
	DeletionReason string
}

// Tombstoned reports whether the definition was deleted.
func (t ManagedTask) Tombstoned() bool { return t.DeletedAt != 0 }

var (
	checkerPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.:+-]{0,63}$`)
	digestPattern   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	scheduleTypes   = map[string]bool{"cron": true, "interval": true, "once": true}
	taskStatusesSet = map[string]bool{"active": true, "paused": true, "done": true}
)

func invalidTask(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidTask, fmt.Sprintf(format, args...))
}

// normalizeManaged fills canonical defaults for unset policy fields.
func normalizeManaged(t *ManagedTask) {
	if t.Status == "" {
		t.Status = "active"
	}
	if t.Kind == TaskKindAgent {
		if t.SessionMode == "" {
			t.SessionMode = SessionIsolated
		}
		if t.DeliveryMode == "" {
			t.DeliveryMode = TaskDeliveryLegacyIPC
		}
	}
	if t.Kind == TaskKindReminder {
		if t.SessionMode == "" {
			t.SessionMode = SessionNone
		}
		if t.DeliveryMode == "" {
			t.DeliveryMode = TaskDeliveryControllerResult
		}
	}
	if t.Overlap == "" {
		t.Overlap = OverlapSkip
	}
	if t.Misfire == "" {
		t.Misfire = MisfireSkip
	}
	if t.MisfireGraceMs == 0 {
		t.MisfireGraceMs = DefaultMisfireGraceMs
	}
	if t.QueueLimit == 0 {
		t.QueueLimit = DefaultQueueLimit
	}
	if t.CatchUpLimit == 0 {
		t.CatchUpLimit = DefaultCatchUpLimit
	}
	if t.DSTFold == "" {
		t.DSTFold = DSTFoldOnce
	}
}

// validateManaged is the strict, kind-aware definition check. It does not look
// at the database; scope and group checks happen in the creating transaction.
func validateManaged(t ManagedTask) error {
	switch t.Kind {
	case TaskKindAgent:
		if strings.TrimSpace(t.Prompt) == "" {
			return invalidTask("agent task needs a prompt")
		}
		if len(t.Prompt) > MaxTaskPromptBytes || !utf8.ValidString(t.Prompt) {
			return invalidTask("prompt is not valid UTF-8 within %d bytes", MaxTaskPromptBytes)
		}
		if t.LiteralText != "" {
			return invalidTask("agent task cannot carry literal reminder text")
		}
		// Explicit main-session execution is a later, separately gated mode.
		if t.SessionMode != SessionIsolated {
			return invalidTask("agent session mode %q is not supported (isolated only)", t.SessionMode)
		}
		if t.DeliveryMode != TaskDeliveryLegacyIPC && t.DeliveryMode != TaskDeliveryControllerResult {
			return invalidTask("delivery mode %q", t.DeliveryMode)
		}
	case TaskKindReminder:
		if t.Prompt != "" {
			return invalidTask("reminder must not carry a model prompt")
		}
		if strings.TrimSpace(t.LiteralText) == "" || len(t.LiteralText) > MaxReminderTextBytes || !utf8.ValidString(t.LiteralText) {
			return invalidTask("reminder text must be 1..%d valid UTF-8 bytes", MaxReminderTextBytes)
		}
		if t.SessionMode != SessionNone || t.DeliveryMode != TaskDeliveryControllerResult {
			return invalidTask("reminder uses no session and controller_result delivery")
		}
	default:
		return invalidTask("kind %q", t.Kind)
	}
	if !t.Conversation.validShape() || t.Conversation.Visibility == "" {
		return invalidTask("destination scope")
	}
	if t.ActorRef == "" || len(t.ActorRef) > MaxActorRefBytes {
		return invalidTask("actor reference must be 1..%d bytes", MaxActorRefBytes)
	}
	if !scheduleTypes[t.ScheduleType] || strings.TrimSpace(t.ScheduleValue) == "" {
		return invalidTask("schedule %q %q", t.ScheduleType, t.ScheduleValue)
	}
	if t.Timezone == "" || len(t.Timezone) > 64 {
		return invalidTask("an explicit IANA timezone is required")
	}
	if _, err := time.LoadLocation(t.Timezone); err != nil {
		return invalidTask("timezone %q: %v", t.Timezone, err)
	}
	if !taskStatusesSet[t.Status] {
		return invalidTask("status %q", t.Status)
	}
	if t.NextRun < 0 || (t.Status == "active" && t.NextRun == 0) {
		return invalidTask("an active task needs a next run cursor")
	}
	switch t.Overlap {
	case OverlapSkip, OverlapCoalesce, OverlapQueue:
	default:
		return invalidTask("overlap policy %q", t.Overlap)
	}
	switch t.Misfire {
	case MisfireSkip, MisfireCoalesce, MisfireCatchUp:
	default:
		return invalidTask("misfire policy %q", t.Misfire)
	}
	if t.DSTFold != DSTFoldOnce && t.DSTFold != DSTFoldBoth {
		return invalidTask("dst fold policy %q", t.DSTFold)
	}
	if t.MisfireGraceMs < 0 || t.MisfireGraceMs > MaxMisfireGraceMs {
		return invalidTask("misfire grace %d ms", t.MisfireGraceMs)
	}
	if t.QueueLimit < 1 || t.QueueLimit > MaxQueueLimit || t.CatchUpLimit < 1 || t.CatchUpLimit > MaxCatchUpLimit {
		return invalidTask("queue and catch-up limits must be 1..%d", MaxQueueLimit)
	}
	if t.ExpiresAt < 0 || t.MaxRuns < 0 {
		return invalidTask("expiry and max runs cannot be negative")
	}
	if len(t.Objective) > MaxTaskTextBytes || len(t.DoneCondition) > MaxTaskTextBytes {
		return invalidTask("objective and done condition are bounded to %d bytes", MaxTaskTextBytes)
	}
	return nil
}

const managedTaskColumns = `id, kind, namespace_id, namespace_epoch, registration_revision, chat_jid, chat_folder, thread_id, visibility,
 actor_ref, prompt, literal_text, session_mode, delivery_mode, schedule_type, schedule_value, timezone, IFNULL(next_run,0), IFNULL(last_run,0), status,
 overlap_policy, misfire_policy, misfire_grace_ms, queue_limit, catch_up_limit, dst_fold_policy, IFNULL(expires_at,0), IFNULL(max_runs,0),
 objective, done_condition, definition_revision, terminal_reason, created_at, IFNULL(deleted_at,0), deletion_reason, execution_version`

func scanManagedTask(row interface{ Scan(...any) error }) (ManagedTask, error) {
	var t ManagedTask
	var kind, vis string
	var literal sql.NullString
	var version int
	sc := &t.Conversation.Scope
	err := row.Scan(&t.ID, &kind, &sc.NamespaceID, &sc.NamespaceEpoch, &sc.RegistrationRevision, &sc.JID, &sc.Folder,
		&t.Conversation.ThreadID, &vis, &t.ActorRef, &t.Prompt, &literal, &t.SessionMode, &t.DeliveryMode,
		&t.ScheduleType, &t.ScheduleValue, &t.Timezone, &t.NextRun, &t.LastRun, &t.Status,
		&t.Overlap, &t.Misfire, &t.MisfireGraceMs, &t.QueueLimit, &t.CatchUpLimit, &t.DSTFold, &t.ExpiresAt, &t.MaxRuns,
		&t.Objective, &t.DoneCondition, &t.Revision, &t.TerminalReason, &t.CreatedAt, &t.DeletedAt, &t.DeletionReason, &version)
	if err != nil {
		return ManagedTask{}, err
	}
	if version != 1 {
		return ManagedTask{}, ErrNotManagedTask
	}
	t.Kind, t.Conversation.Visibility, t.LiteralText = TaskKind(kind), Visibility(vis), literal.String
	return t, nil
}

func readManagedTaskQ(ctx context.Context, q taskSQL, id string) (ManagedTask, error) {
	t, err := scanManagedTask(q.QueryRowContext(ctx, `SELECT `+managedTaskColumns+` FROM scheduled_tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return ManagedTask{}, ErrTaskNotFound
	}
	return t, err
}

// GetManagedTask returns a definition by exact ID, including tombstones (scoped
// operator inspection). A legacy row yields ErrNotManagedTask.
func (s *Store) GetManagedTask(ctx context.Context, id string) (ManagedTask, error) {
	t, err := readManagedTaskQ(ctx, s.taskDB(ctx), id)
	if err != nil && !errors.Is(err, ErrTaskNotFound) && !errors.Is(err, ErrNotManagedTask) {
		return ManagedTask{}, fmt.Errorf("store: GetManagedTask: %w", err)
	}
	return t, err
}

// CreateManagedTask validates and inserts a version 1 definition at revision 1.
// The destination scope is revalidated against the live registration inside the
// transaction, and group chats are refused until CP-020 qualifies them.
func (s *Store) CreateManagedTask(ctx context.Context, t ManagedTask) (ManagedTask, error) {
	normalizeManaged(&t)
	if err := validateManaged(t); err != nil {
		return ManagedTask{}, err
	}
	if t.ID == "" {
		id, err := newUUID()
		if err != nil {
			return ManagedTask{}, err
		}
		t.ID = id
	}
	t.Revision, t.LastRun, t.DeletedAt, t.DeletionReason, t.TerminalReason = 1, 0, 0, "", ""
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		sc := t.Conversation.Scope
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		if err := refuseGroupQ(ctx, q, sc.JID); err != nil {
			return err
		}
		_, err := q.ExecContext(ctx, `INSERT INTO scheduled_tasks
 (id, chat_folder, chat_jid, prompt, schedule_type, schedule_value, next_run, last_run, status, created_at,
  execution_version, definition_revision, kind, session_mode, delivery_mode, namespace_id, namespace_epoch, registration_revision,
  thread_id, visibility, actor_ref, literal_text, timezone, overlap_policy, misfire_policy, misfire_grace_ms, queue_limit,
  catch_up_limit, dst_fold_policy, expires_at, max_runs, objective, done_condition)
 VALUES (?,?,?,?,?,?,?,NULL,?,?, 1,1,?,?,?,?,?,?, ?,?,?,?,?,?,?,?,?, ?,?,?,?,?,?)`,
			t.ID, sc.Folder, sc.JID, t.Prompt, t.ScheduleType, t.ScheduleValue, t.NextRun, t.Status, t.CreatedAt,
			string(t.Kind), t.SessionMode, t.DeliveryMode, sc.NamespaceID, sc.NamespaceEpoch, sc.RegistrationRevision,
			t.Conversation.ThreadID, string(t.Conversation.Visibility), t.ActorRef, nullableString(t.LiteralText), t.Timezone,
			t.Overlap, t.Misfire, t.MisfireGraceMs, t.QueueLimit, t.CatchUpLimit, t.DSTFold,
			nullableInt64(t.ExpiresAt), nullableInt64(t.MaxRuns), t.Objective, t.DoneCondition)
		return err
	})
	if err != nil {
		return ManagedTask{}, fmt.Errorf("store: CreateManagedTask: %w", err)
	}
	return t, nil
}

// refuseGroupQ fails closed for group destinations. Telegram groups have
// negative IDs; observed group metadata is honoured as well.
func refuseGroupQ(ctx context.Context, q taskSQL, jid string) error {
	if strings.HasPrefix(jid, "tg:-") {
		return ErrGroupTaskDisabled
	}
	var group int
	err := q.QueryRowContext(ctx, `SELECT is_group FROM chats WHERE jid=?`, jid).Scan(&group)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if group != 0 {
		return ErrGroupTaskDisabled
	}
	return nil
}

// TaskPatch is a partial definition edit. Nil fields are unchanged. Kind,
// destination, actor and session mode are immutable; create a new task instead.
type TaskPatch struct {
	Prompt, LiteralText                *string
	ScheduleType, ScheduleValue        *string
	Timezone, DeliveryMode             *string
	NextRun                            *int64
	Overlap, Misfire, DSTFold          *string
	MisfireGraceMs, ExpiresAt, MaxRuns *int64
	QueueLimit, CatchUpLimit           *int
	Objective, DoneCondition           *string
}

func (p TaskPatch) apply(t *ManagedTask) {
	set := func(dst *string, v *string) {
		if v != nil {
			*dst = *v
		}
	}
	setI := func(dst *int64, v *int64) {
		if v != nil {
			*dst = *v
		}
	}
	set(&t.Prompt, p.Prompt)
	set(&t.LiteralText, p.LiteralText)
	set(&t.ScheduleType, p.ScheduleType)
	set(&t.ScheduleValue, p.ScheduleValue)
	set(&t.Timezone, p.Timezone)
	set(&t.DeliveryMode, p.DeliveryMode)
	set(&t.Overlap, p.Overlap)
	set(&t.Misfire, p.Misfire)
	set(&t.DSTFold, p.DSTFold)
	set(&t.Objective, p.Objective)
	set(&t.DoneCondition, p.DoneCondition)
	setI(&t.NextRun, p.NextRun)
	setI(&t.MisfireGraceMs, p.MisfireGraceMs)
	setI(&t.ExpiresAt, p.ExpiresAt)
	setI(&t.MaxRuns, p.MaxRuns)
	if p.QueueLimit != nil {
		t.QueueLimit = *p.QueueLimit
	}
	if p.CatchUpLimit != nil {
		t.CatchUpLimit = *p.CatchUpLimit
	}
}

// loadForMutation loads a live managed definition inside folder and checks the
// caller's expected revision. A different folder looks like a missing task so
// a wrong-scope caller learns nothing.
func loadForMutation(ctx context.Context, q taskSQL, folder, id string, expectedRevision int64) (ManagedTask, error) {
	// Folder first: a foreign caller learns nothing about legacy vs missing IDs.
	var owner string
	if err := q.QueryRowContext(ctx, `SELECT chat_folder FROM scheduled_tasks WHERE id = ?`, id).Scan(&owner); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ManagedTask{}, ErrTaskNotFound
		}
		return ManagedTask{}, err
	}
	if owner != folder {
		return ManagedTask{}, ErrTaskNotFound
	}
	t, err := readManagedTaskQ(ctx, q, id)
	if err != nil {
		return ManagedTask{}, err
	}
	if t.Tombstoned() {
		return ManagedTask{}, ErrTaskTombstoned
	}
	if expectedRevision < 1 || t.Revision != expectedRevision {
		return ManagedTask{}, ErrRevisionConflict
	}
	return t, nil
}

// ensureCursorAfterLedger refuses a cursor that would re-admit a slot already
// recorded (admitted or decided) for the task under any earlier revision.
func ensureCursorAfterLedger(ctx context.Context, q taskSQL, taskID string, nextRun int64) error {
	var latest sql.NullInt64
	if err := q.QueryRowContext(ctx, `SELECT max(nominal_at) FROM task_occurrences WHERE task_id=?`, taskID).Scan(&latest); err != nil {
		return err
	}
	if latest.Valid && nextRun <= latest.Int64 {
		return invalidTask("next run %d is not after the latest recorded slot %d", nextRun, latest.Int64)
	}
	return nil
}

// bumpManaged writes the whole mutable definition and increments the revision,
// compare-and-set on the revision the caller loaded.
func bumpManaged(ctx context.Context, q taskSQL, t ManagedTask) (ManagedTask, error) {
	res, err := q.ExecContext(ctx, `UPDATE scheduled_tasks SET
 prompt=?, literal_text=?, schedule_type=?, schedule_value=?, timezone=?, next_run=?, status=?, delivery_mode=?,
 overlap_policy=?, misfire_policy=?, misfire_grace_ms=?, queue_limit=?, catch_up_limit=?, dst_fold_policy=?,
 expires_at=?, max_runs=?, objective=?, done_condition=?, terminal_reason=?, registration_revision=?,
 definition_revision=definition_revision+1
 WHERE id=? AND definition_revision=? AND deleted_at IS NULL`,
		t.Prompt, nullableString(t.LiteralText), t.ScheduleType, t.ScheduleValue, t.Timezone, t.NextRun, t.Status, t.DeliveryMode,
		t.Overlap, t.Misfire, t.MisfireGraceMs, t.QueueLimit, t.CatchUpLimit, t.DSTFold,
		nullableInt64(t.ExpiresAt), nullableInt64(t.MaxRuns), t.Objective, t.DoneCondition, t.TerminalReason,
		t.Conversation.Scope.RegistrationRevision, t.ID, t.Revision)
	if err != nil {
		return ManagedTask{}, err
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return ManagedTask{}, errors.Join(ErrRevisionConflict, e)
	}
	t.Revision++
	return t, nil
}

// UpdateManagedTask applies a patch with compare-and-set on the definition
// revision and returns the new revision. Stale callers get ErrRevisionConflict.
func (s *Store) UpdateManagedTask(ctx context.Context, folder, id string, expectedRevision int64, p TaskPatch) (ManagedTask, error) {
	var out ManagedTask
	_, err := s.withTx(ctx, func(q taskSQL) error {
		t, err := loadForMutation(ctx, q, folder, id, expectedRevision)
		if err != nil {
			return err
		}
		before := t.NextRun
		p.apply(&t)
		normalizeManaged(&t)
		if t.NextRun != before && t.NextRun > 0 {
			if err = ensureCursorAfterLedger(ctx, q, t.ID, t.NextRun); err != nil {
				return err
			}
		}
		if err = validateManaged(t); err != nil {
			return err
		}
		out, err = bumpManaged(ctx, q, t)
		return err
	})
	if err != nil {
		return ManagedTask{}, fmt.Errorf("store: UpdateManagedTask: %w", err)
	}
	return out, nil
}

// SetManagedTaskStatus pauses or resumes future scheduling. It is the schedule
// "cancel" intent: it never touches an admitted run (see RequestRunStop). A
// resume supplies the next cursor (0 keeps the stored one).
func (s *Store) SetManagedTaskStatus(ctx context.Context, folder, id string, expectedRevision int64, status string, nextRun int64) (ManagedTask, error) {
	if status != "active" && status != "paused" {
		return ManagedTask{}, invalidTask("status %q", status)
	}
	var out ManagedTask
	_, err := s.withTx(ctx, func(q taskSQL) error {
		t, err := loadForMutation(ctx, q, folder, id, expectedRevision)
		if err != nil {
			return err
		}
		if t.Status == "done" {
			return invalidTask("a finished task cannot be resumed")
		}
		t.Status = status
		if status == "active" {
			if nextRun > 0 {
				t.NextRun = nextRun
			}
			if t.NextRun > 0 {
				if err = ensureCursorAfterLedger(ctx, q, t.ID, t.NextRun); err != nil {
					return err
				}
			}
			t.TerminalReason = ""
		}
		if err = validateManaged(t); err != nil {
			return err
		}
		out, err = bumpManaged(ctx, q, t)
		return err
	})
	if err != nil {
		return ManagedTask{}, fmt.Errorf("store: SetManagedTaskStatus: %w", err)
	}
	return out, nil
}

// RebindManagedTaskScope adopts a newer registration revision of the same
// namespace, epoch, chat and folder after an operator reviewed it (a chat
// rename advances the revision and makes admission fail closed until then).
// A different namespace identity is never accepted.
func (s *Store) RebindManagedTaskScope(ctx context.Context, folder, id string, expectedRevision int64, to ScopeRef) (ManagedTask, error) {
	var out ManagedTask
	_, err := s.withTx(ctx, func(q taskSQL) error {
		t, err := loadForMutation(ctx, q, folder, id, expectedRevision)
		if err != nil {
			return err
		}
		cur := t.Conversation.Scope
		if to.NamespaceID != cur.NamespaceID || to.NamespaceEpoch != cur.NamespaceEpoch || to.JID != cur.JID || to.Folder != cur.Folder {
			return ErrScopeIdentityChanged
		}
		if err = validateScopeQ(ctx, q, to); err != nil {
			return err
		}
		t.Conversation.Scope = to
		out, err = bumpManaged(ctx, q, t)
		return err
	})
	if err != nil {
		return ManagedTask{}, fmt.Errorf("store: RebindManagedTaskScope: %w", err)
	}
	return out, nil
}

// DueManagedTasks lists active, undeleted managed definitions whose cursor is
// due, oldest first. Legacy rows are never returned (GetDueTasks excludes
// managed rows), so the two scheduler paths cannot both claim a task.
func (s *Store) DueManagedTasks(ctx context.Context, now int64, limit int) ([]ManagedTask, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT `+managedTaskColumns+` FROM scheduled_tasks
 WHERE execution_version=1 AND status='active' AND deleted_at IS NULL AND next_run > 0 AND next_run <= ?
 ORDER BY next_run ASC LIMIT ?`, now, limit)
	if err != nil {
		return nil, fmt.Errorf("store: DueManagedTasks: %w", err)
	}
	defer rows.Close()
	var out []ManagedTask
	for rows.Next() {
		t, e := scanManagedTask(rows)
		if e != nil {
			return nil, fmt.Errorf("store: DueManagedTasks: %w", e)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// DeleteManagedTask tombstones a definition (see tombstoneTaskQ). It is
// idempotent only through ErrTaskTombstoned; a second call reports it.
func (s *Store) DeleteManagedTask(ctx context.Context, folder, id string, expectedRevision int64, reason string) error {
	if !reasonPattern.MatchString(reason) {
		return invalidTask("deletion reason must be a fixed machine code")
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		t, err := loadForMutation(ctx, q, folder, id, expectedRevision)
		if err != nil {
			return err
		}
		return s.tombstoneTaskQ(ctx, q, t, reason)
	})
	if err != nil {
		return fmt.Errorf("store: DeleteManagedTask: %w", err)
	}
	return nil
}

// tombstoneTaskQ disables future admission, cancels proven-never-started work
// and requests a stop for everything else, in the caller's transaction. It
// keeps run, result, outbox and verification evidence: a started or uncertain
// attempt stays execution_unknown with its reservation until real exit or
// reconciliation, and a possibly sent reminder is never relabelled unsent.
func (s *Store) tombstoneTaskQ(ctx context.Context, q taskSQL, t ManagedTask, reason string) error {
	now := time.Now().UnixMilli()
	runs, err := liveRunsQ(ctx, q, t.ID)
	if err != nil {
		return err
	}
	for _, r := range runs {
		if _, err = requestRunStopQ(ctx, q, r, "task_deleted", now); err != nil {
			return err
		}
	}
	if err = s.cancelPendingDeliveriesQ(ctx, q, t.ID); err != nil {
		return err
	}
	res, err := q.ExecContext(ctx, `UPDATE scheduled_tasks SET status='done', next_run=0, deleted_at=?, deletion_reason=?,
 terminal_reason='deleted', definition_revision=definition_revision+1
 WHERE id=? AND definition_revision=? AND deleted_at IS NULL`, now, reason, t.ID, t.Revision)
	if err != nil {
		return err
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return errors.Join(ErrRevisionConflict, e)
	}
	return nil
}

// cancelPendingDeliveriesQ asks CP-002 to cancel still-pending reminder
// deliveries; sending and uncertain parts keep their state.
func (s *Store) cancelPendingDeliveriesQ(ctx context.Context, q taskSQL, taskID string) error {
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT o.delivery_id FROM task_occurrences o
 JOIN delivery_groups d ON d.delivery_id=o.delivery_id WHERE o.task_id=? AND d.unresolved=1 AND d.state='pending'`, taskID)
	if err != nil {
		return err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	inner := s.txContext(ctx, q)
	for _, id := range ids {
		if _, err = s.CancelDelivery(inner, id, "task_deleted"); err != nil {
			return err
		}
	}
	return nil
}

// txContext makes q the Store transaction for helpers that resolve it from ctx.
func (s *Store) txContext(ctx context.Context, q taskSQL) context.Context {
	if tx, ok := q.(*sql.Tx); ok {
		return context.WithValue(ctx, taskTransactionKey{}, taskTransaction{owner: s, tx: tx})
	}
	return ctx
}
