package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// Provisional agent reports (CP-004/021 plan PR 3).
//
// A report is the model's own claim about a run. It is stored beside the run as
// model_reported metadata and never changes the execution state, the
// publication state, delivery, the frozen result or any host verification. It is
// accepted only while the reporting attempt is the run's live running attempt:
// a settled, cancelled or foreign attempt cannot write.

// Goal statuses a report may carry.
const (
	GoalAchieved = "achieved"
	GoalPartial  = "partial"
	GoalBlocked  = "blocked"
	GoalUnknown  = "unknown"
)

// ErrReportRejected: the attempt is not the live running attempt of the run, or
// the revision is stale. The caller's detail never reaches the agent.
var ErrReportRejected = errors.New("store: provisional report rejected")

// ProvisionalReport is already validated and canonicalized by the caller.
type ProvisionalReport struct {
	RunID     string
	AttemptID string
	// Status is empty when the report carries no outcome.
	Status string
	// OutcomeJSON and CheckpointJSON are canonical JSON, empty when absent.
	OutcomeJSON    string
	CheckpointJSON string
	// PlanVersion is the checkpoint's planVersion (0 without a checkpoint).
	PlanVersion int
}

// RecordProvisionalReport stores the latest bounded revision of a report.
// Outcome and checkpoint are replaced independently; an absent part leaves the
// stored one alone. A checkpoint older than the stored one is rejected.
func (s *Store) RecordProvisionalReport(ctx context.Context, r ProvisionalReport) error {
	if r.RunID == "" || r.AttemptID == "" || (r.OutcomeJSON == "" && r.CheckpointJSON == "") {
		return fmt.Errorf("%w: empty report", ErrReportRejected)
	}
	switch r.Status {
	case "", GoalAchieved, GoalPartial, GoalBlocked, GoalUnknown:
	default:
		return fmt.Errorf("%w: status", ErrReportRejected)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var attState, runState, storedCP string
		err := q.QueryRowContext(ctx, `SELECT a.execution_state, r.execution_state, IFNULL(r.checkpoint_json,'')
 FROM task_attempts a JOIN task_runs r ON r.run_id=a.run_id WHERE a.attempt_id=? AND a.run_id=?`,
			r.AttemptID, r.RunID).Scan(&attState, &runState, &storedCP)
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("%w: unknown attempt", ErrReportRejected)
		}
		if err != nil {
			return err
		}
		if attState != string(ExecRunning) || runState != string(ExecRunning) {
			return fmt.Errorf("%w: attempt is not live", ErrReportRejected)
		}
		if r.CheckpointJSON != "" && storedCP != "" {
			var old struct {
				PlanVersion int `json:"planVersion"`
			}
			if json.Unmarshal([]byte(storedCP), &old) == nil && r.PlanVersion < old.PlanVersion {
				return fmt.Errorf("%w: stale checkpoint", ErrReportRejected)
			}
		}
		if r.OutcomeJSON != "" {
			authority := "model_reported"
			if r.Status == GoalUnknown || r.Status == "" {
				authority = "none"
			}
			status := r.Status
			if status == "" {
				status = GoalUnknown
			}
			if _, e := q.ExecContext(ctx, `UPDATE task_runs SET goal_status=?, goal_authority=?, outcome_json=? WHERE run_id=?`,
				status, authority, r.OutcomeJSON, r.RunID); e != nil {
				return e
			}
		}
		if r.CheckpointJSON != "" {
			if _, e := q.ExecContext(ctx, `UPDATE task_runs SET checkpoint_json=? WHERE run_id=?`, r.CheckpointJSON, r.RunID); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		if errors.Is(err, ErrReportRejected) {
			return err
		}
		return fmt.Errorf("store: RecordProvisionalReport: %w", err)
	}
	return nil
}
