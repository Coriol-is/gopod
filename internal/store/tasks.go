package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// TaskRecord is one row in the scheduled_tasks table.
type TaskRecord struct {
	ID            string
	ChatFolder    string
	ChatJID       string
	Prompt        string
	ScheduleType  string // "cron", "interval", "once"
	ScheduleValue string // cron expr, duration string, or RFC3339
	NextRun       int64  // unix ms, 0 = not scheduled
	LastRun       int64  // unix ms, 0 = never
	Status        string // "active", "paused", "done"
	CreatedAt     int64  // unix ms
}

// TaskRunLog is one row in the task_run_logs table.
type TaskRunLog struct {
	TaskID     string
	RunAt      int64 // unix ms
	DurationMs int64
	Status     string // "success", "error"
	Result     string
	Error      string
	TurnID     int64
}

// ErrTaskNotFound is returned when a task ID doesn't exist.
var ErrTaskNotFound = errors.New("store: task not found")

// ErrTaskRunNotFound is returned when no task_run_logs row matches
// (task_id, run_at).
var ErrTaskRunNotFound = errors.New("store: task run not found")

// CreateTask inserts a new scheduled task and returns its generated ID.
func (s *Store) CreateTask(ctx context.Context, t TaskRecord) (string, error) {
	if t.ChatFolder == "" {
		return "", errors.New("store: CreateTask: empty ChatFolder")
	}
	if t.Prompt == "" {
		return "", errors.New("store: CreateTask: empty Prompt")
	}
	if t.ID == "" {
		t.ID = uuid.New().String()
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	if t.Status == "" {
		t.Status = "active"
	}

	const stmt = `
		INSERT INTO scheduled_tasks
		  (id, chat_folder, chat_jid, prompt, schedule_type, schedule_value,
		   next_run, last_run, status, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`

	if _, err := s.taskDB(ctx).ExecContext(ctx, stmt,
		t.ID, t.ChatFolder, t.ChatJID, t.Prompt,
		t.ScheduleType, t.ScheduleValue,
		t.NextRun, nullableInt64(t.LastRun),
		t.Status, t.CreatedAt,
	); err != nil {
		return "", fmt.Errorf("store: CreateTask: %w", err)
	}
	return t.ID, nil
}

// GetTask returns a single task by ID.
func (s *Store) GetTask(ctx context.Context, id string) (TaskRecord, error) {
	const q = `
		SELECT id, chat_folder, chat_jid, prompt, schedule_type, schedule_value,
		       next_run, IFNULL(last_run, 0), status, created_at
		  FROM scheduled_tasks WHERE id = ?`
	var t TaskRecord
	err := s.taskDB(ctx).QueryRowContext(ctx, q, id).Scan(
		&t.ID, &t.ChatFolder, &t.ChatJID, &t.Prompt,
		&t.ScheduleType, &t.ScheduleValue,
		&t.NextRun, &t.LastRun, &t.Status, &t.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRecord{}, ErrTaskNotFound
	}
	if err != nil {
		return TaskRecord{}, fmt.Errorf("store: GetTask: %w", err)
	}
	return t, nil
}

// ListTasksByChat returns all tasks for a chat folder, ordered by
// created_at ascending.
func (s *Store) ListTasksByChat(ctx context.Context, chatFolder string) ([]TaskRecord, error) {
	const q = `
		SELECT id, chat_folder, chat_jid, prompt, schedule_type, schedule_value,
		       next_run, IFNULL(last_run, 0), status, created_at
		  FROM scheduled_tasks
		 WHERE chat_folder = ? AND deleted_at IS NULL
		 ORDER BY created_at ASC`
	rows, err := s.taskDB(ctx).QueryContext(ctx, q, chatFolder)
	if err != nil {
		return nil, fmt.Errorf("store: ListTasksByChat: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// GetDueTasks returns all active legacy tasks whose next_run ≤ now. Managed
// (execution_version 1) definitions are admitted only through
// AdmitTaskOccurrence; see DueManagedTasks.
// Ordered by next_run ascending (oldest-due first).
func (s *Store) GetDueTasks(ctx context.Context, now int64) ([]TaskRecord, error) {
	const q = `
		SELECT id, chat_folder, chat_jid, prompt, schedule_type, schedule_value,
		       next_run, IFNULL(last_run, 0), status, created_at
		  FROM scheduled_tasks
		 WHERE status = 'active' AND next_run > 0 AND next_run <= ?
		   AND execution_version = 0
		 ORDER BY next_run ASC`
	rows, err := s.taskDB(ctx).QueryContext(ctx, q, now)
	if err != nil {
		return nil, fmt.Errorf("store: GetDueTasks: %w", err)
	}
	defer rows.Close()
	return scanTasks(rows)
}

// UpdateTaskStatus sets the status and optionally next_run/last_run.
func (s *Store) UpdateTaskStatus(ctx context.Context, id, status string, nextRun, lastRun int64) error {
	const stmt = `
		UPDATE scheduled_tasks
		   SET status = ?, next_run = ?, last_run = ?
		 WHERE id = ? AND execution_version = 0`
	res, err := s.taskDB(ctx).ExecContext(ctx, stmt, status, nextRun, lastRun, id)
	if err != nil {
		return fmt.Errorf("store: UpdateTaskStatus: %w", err)
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return s.legacyMutationMiss(ctx, id)
	}
	return nil
}

// LogTaskRun inserts a row into task_run_logs.
func (s *Store) LogTaskRun(ctx context.Context, log TaskRunLog) error {
	const stmt = `
		INSERT INTO task_run_logs (task_id, run_at, duration_ms, status, result, error)
		VALUES (?, ?, ?, ?, ?, ?)`
	if _, err := s.taskDB(ctx).ExecContext(ctx, stmt,
		log.TaskID, log.RunAt, log.DurationMs,
		log.Status, nullableString(log.Result), nullableString(log.Error),
	); err != nil {
		return fmt.Errorf("store: LogTaskRun: %w", err)
	}
	return nil
}

// SetTaskRunTurn links a task_run_logs row to the turn that executes it.
// Rows are addressed by (task_id, run_at), unique per scheduled slot.
func (s *Store) SetTaskRunTurn(ctx context.Context, taskID string, runAt, turnID int64) error {
	res, err := s.taskDB(ctx).ExecContext(ctx,
		`UPDATE task_run_logs SET turn_id = ? WHERE task_id = ? AND run_at = ?`,
		turnID, taskID, runAt)
	if err != nil {
		return fmt.Errorf("store: SetTaskRunTurn: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: SetTaskRunTurn rows affected: %w", err)
	}
	if n == 0 {
		return ErrTaskRunNotFound
	}
	return nil
}

// FinishTaskRun records the outcome of a task run that was logged as
// "running" when it was enqueued.
func (s *Store) FinishTaskRun(ctx context.Context, taskID string, runAt int64, status string, durationMs int64, errText string) error {
	res, err := s.taskDB(ctx).ExecContext(ctx,
		`UPDATE task_run_logs SET status = ?, duration_ms = ?, error = ?
		  WHERE task_id = ? AND run_at = ?`,
		status, durationMs, nullableString(errText), taskID, runAt)
	if err != nil {
		return fmt.Errorf("store: FinishTaskRun: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("store: FinishTaskRun rows affected: %w", err)
	}
	if n == 0 {
		return ErrTaskRunNotFound
	}
	return nil
}

// GetTaskRun returns one task_run_logs row by (task_id, run_at).
func (s *Store) GetTaskRun(ctx context.Context, taskID string, runAt int64) (TaskRunLog, error) {
	var l TaskRunLog
	var dur, turnID sql.NullInt64
	var result, errText sql.NullString
	err := s.taskDB(ctx).QueryRowContext(ctx,
		`SELECT task_id, run_at, duration_ms, status, result, error, turn_id
		   FROM task_run_logs WHERE task_id = ? AND run_at = ?`, taskID, runAt).
		Scan(&l.TaskID, &l.RunAt, &dur, &l.Status, &result, &errText, &turnID)
	if errors.Is(err, sql.ErrNoRows) {
		return TaskRunLog{}, ErrTaskRunNotFound
	}
	if err != nil {
		return TaskRunLog{}, fmt.Errorf("store: GetTaskRun: %w", err)
	}
	l.DurationMs = dur.Int64
	l.Result = result.String
	l.Error = errText.String
	l.TurnID = turnID.Int64
	return l, nil
}

// DeleteTask removes a task. No error if it doesn't exist.
//
// A managed (execution_version 1) definition is refused with ErrManagedTask: it
// is never physically deleted and is tombstoned only through DeleteManagedTask
// with an expected revision. A legacy row is
// deleted physically only while no managed ledger row references it, checked in
// the same transaction as the delete.
func (s *Store) DeleteTask(ctx context.Context, id string) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var version int
		err := q.QueryRowContext(ctx, `SELECT execution_version FROM scheduled_tasks WHERE id = ?`, id).Scan(&version)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if version == 1 {
			// Managed deletion needs an expected revision: DeleteManagedTask.
			return ErrManagedTask
		}
		var refs int
		if err = q.QueryRowContext(ctx, `SELECT (SELECT count(*) FROM task_runs WHERE task_id=?) + (SELECT count(*) FROM task_occurrences WHERE task_id=?)`, id, id).Scan(&refs); err != nil {
			return err
		}
		if refs > 0 {
			return ErrTaskHasManagedEvidence
		}
		_, err = q.ExecContext(ctx, `DELETE FROM scheduled_tasks WHERE id = ? AND execution_version = 0`, id)
		return err
	})
	if err != nil {
		return fmt.Errorf("store: DeleteTask: %w", err)
	}
	return nil
}

// legacyMutationMiss explains why a legacy mutator touched no row: a managed
// definition must change through the revisioned API, anything else is absent.
func (s *Store) legacyMutationMiss(ctx context.Context, id string) error {
	var version int
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT execution_version FROM scheduled_tasks WHERE id = ?`, id).Scan(&version)
	if err == nil && version == 1 {
		return ErrManagedTask
	}
	return ErrTaskNotFound
}

func scanTasks(rows *sql.Rows) ([]TaskRecord, error) {
	var out []TaskRecord
	for rows.Next() {
		var t TaskRecord
		if err := rows.Scan(
			&t.ID, &t.ChatFolder, &t.ChatJID, &t.Prompt,
			&t.ScheduleType, &t.ScheduleValue,
			&t.NextRun, &t.LastRun, &t.Status, &t.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("store: scanTasks: %w", err)
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpdateTaskDefinition changes an existing task without allowing its ownership
// or destination to move. The caller has already validated its source scope.
func (s *Store) UpdateTaskDefinition(ctx context.Context, t TaskRecord) error {
	res, err := s.taskDB(ctx).ExecContext(ctx, `UPDATE scheduled_tasks SET prompt=?,schedule_type=?,schedule_value=?,next_run=? WHERE id=? AND chat_folder=? AND execution_version=0`, t.Prompt, t.ScheduleType, t.ScheduleValue, t.NextRun, t.ID, t.ChatFolder)
	if err != nil {
		return fmt.Errorf("store: update task definition: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return s.legacyMutationMiss(ctx, t.ID)
	}
	return nil
}
