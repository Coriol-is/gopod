package store

import (
	"context"
	"fmt"
)

// IPCRequestStatus is the durable state of one accepted IPC message request,
// joined with its delivery. It carries no payload, path or raw error.
type IPCRequestStatus struct {
	Key           string
	DeliveryID    string
	DeliveryState string
	Unresolved    bool
}

// RecentIPCRequests lists the newest accepted IPC requests of a namespace and
// epoch with their current delivery state, so the host can recreate receipts
// from durable state after a crash. Rejected tombstones are not listed: their
// receipt is written when the rejection is recorded and replayed from the
// request file. The scope must still be current.
func (s *Store) RecentIPCRequests(ctx context.Context, sc ScopeRef, limit int) ([]IPCRequestStatus, error) {
	if limit <= 0 {
		return nil, nil
	}
	var out []IPCRequestStatus
	_, err := s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		rows, err := q.QueryContext(ctx, `SELECT r.request_key, d.delivery_id, d.state, d.unresolved
 FROM ipc_message_requests r JOIN delivery_groups d ON d.delivery_id = r.delivery_id
 WHERE r.namespace_id=? AND r.namespace_epoch=? AND r.outcome='accepted'
 ORDER BY r.created_at DESC, r.request_key DESC LIMIT ?`, sc.NamespaceID, sc.NamespaceEpoch, limit)
		if err != nil {
			return fmt.Errorf("store: list ipc requests: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var r IPCRequestStatus
			var unresolved int
			if err := rows.Scan(&r.Key, &r.DeliveryID, &r.DeliveryState, &unresolved); err != nil {
				return err
			}
			r.Unresolved = unresolved != 0
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
