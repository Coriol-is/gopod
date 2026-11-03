package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// OrdinaryRecoveryAction separates native execution from immutable result delivery.
type OrdinaryRecoveryAction string

const (
	OrdinaryDispatch  OrdinaryRecoveryAction = "dispatch"
	OrdinaryFrozen    OrdinaryRecoveryAction = "frozen"
	OrdinaryPublished OrdinaryRecoveryAction = "published"
	OrdinarySettled   OrdinaryRecoveryAction = "settled"
)

// OrdinaryRecovery never treats publication (including failure or uncertainty)
// as remote delivery. Published means the outbox owns all further delivery work.
type OrdinaryRecovery struct {
	Action        OrdinaryRecoveryAction
	ResultID      string
	DeliveryID    string
	DeliveryState string
}

// RecoverOrdinaryTurn is a boot-only reconciliation, before producers start.
// It never invokes a model or invents a delivery plan. Unpublished frozen results
// remain unfinished for a parity-qualified planner; the queue must hold their
// folder. Once a plan exists, execution is complete and the existing dispatcher
// owns delivery without replay, regardless of the plan's current outcome.
// Managed attempts belong exclusively to RecoverTaskAttempt.
func (s *Store) RecoverOrdinaryTurn(ctx context.Context, id int64) (OrdinaryRecovery, error) {
	var plan OrdinaryRecovery
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var status, folder string
		var attempts, reply, managed, thread int
		err := q.QueryRowContext(ctx, `SELECT status,chat_folder,thread_id,attempts,reply_msg_id,
EXISTS(SELECT 1 FROM task_attempts a WHERE a.turn_id=turns.id) FROM turns WHERE id=?`, id).
			Scan(&status, &folder, &thread, &attempts, &reply, &managed)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrTurnNotFound
		}
		if err != nil {
			return err
		}
		if managed != 0 {
			return fmt.Errorf("ordinary recovery: managed attempt: %w", ErrTurnBatchConflict)
		}
		if status != TurnPending && status != TurnRunning && status != TurnInterrupted {
			plan.Action = OrdinarySettled
			return nil
		}
		// Any earlier freeze forbids another native attempt, even if a stale caller
		// subsequently advanced the counter. The latest result is never regenerated.
		err = q.QueryRowContext(ctx, `SELECT result_id FROM turn_result_members WHERE turn_id=? ORDER BY attempt DESC LIMIT 1`, id).Scan(&plan.ResultID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		finish, reason := "", ""
		if plan.ResultID != "" {
			result, err := readTurnResult(ctx, q, plan.ResultID)
			if err != nil {
				return err
			}
			if result.Conversation.Scope.Folder != folder || result.Conversation.ThreadID != thread {
				return ErrScopeMismatch
			}
			err = q.QueryRowContext(ctx, `SELECT delivery_id FROM delivery_groups WHERE result_id=?`, plan.ResultID).Scan(&plan.DeliveryID)
			if errors.Is(err, sql.ErrNoRows) {
				plan.Action = OrdinaryFrozen
				return nil
			}
			if err != nil {
				return err
			}
			delivery, err := readDelivery(ctx, q, plan.DeliveryID)
			if err != nil {
				return err
			}
			if delivery.Owner != OwnerControllerResult || !sameConversation(result.Conversation, delivery.Conversation) {
				return ErrScopeMismatch
			}
			plan.Action, plan.DeliveryState = OrdinaryPublished, delivery.State
			finish = TurnDone
		} else if attempts > 0 || reply != 0 {
			plan.Action = OrdinarySettled
			finish, reason = TurnFailed, "execution_unknown: controller restarted after dispatch; automatic replay refused"
			if reply != 0 {
				finish, reason = TurnDone, ""
			}
		} else {
			plan.Action = OrdinaryDispatch
			return nil
		}
		_, err = q.ExecContext(ctx, `UPDATE turns SET status=?,error=?,finished_at=? WHERE id=?`, finish, nullableString(reason), time.Now().UnixMilli(), id)
		return err
	})
	if err != nil {
		return OrdinaryRecovery{}, fmt.Errorf("recovering ordinary turn %d: %w", id, err)
	}
	return plan, nil
}

// TurnBatchIneligibleError requests a queue hold, not retry or completion. A
// mixed batch must remain intact until reconciliation can classify each member.
type TurnBatchIneligibleError struct{ TurnID int64 }

func (e *TurnBatchIneligibleError) Error() string {
	return fmt.Sprintf("turn %d requires result/terminal reconciliation", e.TurnID)
}
func (e *TurnBatchIneligibleError) HoldBatch() bool { return true }
