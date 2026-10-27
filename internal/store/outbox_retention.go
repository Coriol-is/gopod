package store

import (
	"context"
	"fmt"
	"time"
)

// DefaultTerminalPayloadRetention is how long terminal delivery payloads are kept.
const DefaultTerminalPayloadRetention = 30 * 24 * time.Hour

func checkOutboxQuota(ctx context.Context, q taskSQL, limits OutboxLimits, namespaceID string) error {
	var global, local int
	if err := q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(namespace_id=?),0) FROM delivery_groups WHERE unresolved=1`, namespaceID).Scan(&global, &local); err != nil {
		return fmt.Errorf("store: outbox quota: %w", err)
	}
	if global >= limits.MaxUnresolvedGlobal || local >= limits.MaxUnresolvedPerNamespace {
		return ErrOutboxQuotaExceeded
	}
	return nil
}

// CompactTerminalDeliveries drops payloads and attempt diagnostics of
// terminal (fully resolved) groups last updated before now-retention, at most
// limit groups per call. The group row, part states, remote ids, plan hash and
// every ipc_message_requests tombstone remain, so dedupe and status stay truthful.
// Unresolved groups (pending, sending, uncertain) are never touched. It returns
// the number of groups compacted.
func (s *Store) CompactTerminalDeliveries(ctx context.Context, now time.Time, retention time.Duration, limit int) (int, error) {
	if retention <= 0 {
		retention = DefaultTerminalPayloadRetention
	}
	if limit <= 0 {
		limit = 100
	}
	cutoff := now.Add(-retention).UnixMilli()
	compacted := 0
	_, err := s.withTx(ctx, func(q taskSQL) error {
		rows, err := q.QueryContext(ctx, `SELECT delivery_id FROM delivery_groups WHERE unresolved=0 AND compacted_at IS NULL AND updated_at<? ORDER BY updated_at LIMIT ?`, cutoff, limit)
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
		for _, id := range ids {
			if _, err = q.ExecContext(ctx, `DELETE FROM outbox_attempts WHERE part_id IN (SELECT part_id FROM outbox_parts WHERE delivery_id=?)`, id); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET payload=NULL WHERE delivery_id=?`, id); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE delivery_groups SET compacted_at=? WHERE delivery_id=?`, now.UnixMilli(), id); err != nil {
				return err
			}
		}
		compacted = len(ids)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: compact deliveries: %w", err)
	}
	return compacted, nil
}
