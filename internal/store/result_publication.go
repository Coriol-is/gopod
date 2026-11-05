package store

import "context"

// ErrResultStopped holds an unpublished interactive result after a durable Stop.
// The result and its immutable intent remain evidence; it must not be regenerated.
var ErrResultStopped = resultStoppedError{}

type resultStoppedError struct{}

func (resultStoppedError) Error() string  { return "store: result publication held by stopped turn" }
func (resultStoppedError) NoReplay() bool { return true }

// checkResultPublicationQ runs in the same transaction as new publication.
// Any stopped member holds the whole coalesced result. A later attempt counter
// cannot erase that evidence. Managed results retain their separate ledger rules.
// Already published results are returned before this check: their outbox state,
// including sending/uncertain effects, must not be reclassified by a turn status.
func checkResultPublicationQ(ctx context.Context, q taskSQL, result TurnResult) error {
	if result.Kind != ExecInteractive {
		return nil
	}
	var stopped int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM turn_result_members m JOIN turns t ON t.id=m.turn_id WHERE m.result_id=? AND (t.status=? OR EXISTS(SELECT 1 FROM ordinary_stop_members s WHERE s.turn_id=m.turn_id))`, result.ID, TurnStopped).Scan(&stopped); err != nil {
		return err
	}
	if stopped > 0 {
		return ErrResultStopped
	}
	return nil
}
