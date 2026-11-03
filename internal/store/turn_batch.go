package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ErrTurnBatchConflict means the queue snapshot is no longer eligible to start.
var ErrTurnBatchConflict = errors.New("store: turn batch cannot start from this snapshot")

// TurnStart identifies a queued turn and the previously committed attempt count.
// The queue supplies these fields from admission/recovery, never model output.
type TurnStart struct {
	ID       int64
	Folder   string
	ThreadID int
	Source   string
	Attempts int
}

// StartTurnBatch atomically stamps a non-managed batch and returns its committed
// attempt numbers in input order. Any rejected member rolls back the whole batch.
// Terminal, acknowledged and result-bearing turns are fenced in the transaction.
// Reconciliation is separate; a rejected mixed batch is never partially started.
// This does not freeze or publish a result, or grant permission to replay an
// interrupted native execution. Startup reconciliation remains the caller's job.
func (s *Store) StartTurnBatch(ctx context.Context, batch []TurnStart) ([]int, error) {
	if len(batch) == 0 {
		return nil, ErrTurnBatchConflict
	}
	attempts := make([]int, len(batch))
	_, err := s.withTx(ctx, func(q taskSQL) error {
		seen := make(map[int64]bool, len(batch))
		started := time.Now().UnixMilli()
		for i, it := range batch {
			if it.ID <= 0 || it.Attempts < 0 || seen[it.ID] || it.Folder != batch[0].Folder || it.ThreadID != batch[0].ThreadID || (it.Source == "task") != (batch[0].Source == "task") {
				return ErrTurnBatchConflict
			}
			seen[it.ID] = true
			var folder, source, status string
			var thread, previous, managed, reply, frozen int
			err := q.QueryRowContext(ctx, `SELECT chat_folder,thread_id,source,attempts,status,reply_msg_id,
    EXISTS(SELECT 1 FROM turn_result_members m WHERE m.turn_id=turns.id),
    EXISTS(SELECT 1 FROM task_attempts a WHERE a.turn_id=turns.id) FROM turns WHERE id=?`, it.ID).
				Scan(&folder, &thread, &source, &previous, &status, &reply, &frozen, &managed)
			if errors.Is(err, sql.ErrNoRows) {
				return ErrTurnNotFound
			}
			if err != nil {
				return err
			}
			if folder != it.Folder || thread != it.ThreadID || source != it.Source || previous != it.Attempts || managed != 0 {
				return ErrTurnBatchConflict
			}
			neverStartedInterrupted := status == TurnInterrupted && previous == 0
			if (status != TurnPending && status != TurnRunning && !neverStartedInterrupted) || reply != 0 || frozen != 0 {
				return &TurnBatchIneligibleError{TurnID: it.ID}
			}
			if _, err := q.ExecContext(ctx, `UPDATE turns SET status='running',attempts=attempts+1,started_at=? WHERE id=?`, started, it.ID); err != nil {
				return err
			}
			attempts[i] = previous + 1
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("starting turn batch: %w", err)
	}
	return attempts, nil
}
