package store

import (
	"context"
	"database/sql"
	"fmt"
)

// BackupArtifact is one artifact inventory row as stored in a database file,
// read without Open (so without migrations). It carries no agent-chosen paths.
type BackupArtifact struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	BlobKey string `json:"blob_key"`
	Size    int64  `json:"size"`
	SHA256  string `json:"sha256"`
}

// ArtifactInventory lists artifacts that a backup must account for (ready and
// quarantined) from the database file at path. A database without the artifact
// tables yields an empty inventory.
func ArtifactInventory(ctx context.Context, path string) ([]BackupArtifact, error) {
	db, err := openSnapshotDB(path, "mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()
	ok, err := hasTable(ctx, db, "artifacts")
	if err != nil || !ok {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `SELECT artifact_id, state, relative_blob_key, size, sha256
 FROM artifacts WHERE state IN ('ready','quarantined') ORDER BY artifact_id`)
	if err != nil {
		return nil, fmt.Errorf("store: artifact inventory: %w", err)
	}
	defer rows.Close()
	var out []BackupArtifact
	for rows.Next() {
		var a BackupArtifact
		if err := rows.Scan(&a.ID, &a.State, &a.BlobKey, &a.Size, &a.SHA256); err != nil {
			return nil, fmt.Errorf("store: artifact inventory: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func hasTable(ctx context.Context, db *sql.DB, name string) (bool, error) {
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&n); err != nil {
		return false, fmt.Errorf("store: probe table %s: %w", name, err)
	}
	return n > 0, nil
}

// WorkEvidence counts durable work that was not settled when the database was
// read. It is evidence recorded in a backup manifest, not a lock: a stopped
// store keeps interrupted rows until the next start reconciles them. Counts
// only; no message or task content is read.
type WorkEvidence struct {
	TurnsPending         int64 `json:"turns_pending"`
	TurnsRunning         int64 `json:"turns_running"`
	OutboxPartsInFlight  int64 `json:"outbox_parts_in_flight"` // pending, sending or uncertain
	DeliveriesUnresolved int64 `json:"deliveries_unresolved"`
	ArtifactReservations int64 `json:"artifact_reservations_active"`
	TaskRunsExecuting    int64 `json:"task_runs_executing"` // running, cancel_requested or execution_unknown
	TaskAttemptsHeld     int64 `json:"task_attempts_held"`
}

// Unsettled reports whether any counter is non-zero.
func (e WorkEvidence) Unsettled() bool { return e != WorkEvidence{} }

// ReadWorkEvidence reads WorkEvidence from the database file at path
// read-only. Tables that do not exist yet count as zero.
func ReadWorkEvidence(ctx context.Context, path string) (WorkEvidence, error) {
	var e WorkEvidence
	db, err := openSnapshotDB(path, "mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return e, err
	}
	defer db.Close()
	probes := []struct {
		table string
		query string
		dst   *int64
	}{
		{"turns", `SELECT count(*) FROM turns WHERE status='pending'`, &e.TurnsPending},
		{"turns", `SELECT count(*) FROM turns WHERE status='running'`, &e.TurnsRunning},
		{"outbox_parts", `SELECT count(*) FROM outbox_parts WHERE state IN ('pending','sending','uncertain')`, &e.OutboxPartsInFlight},
		{"delivery_groups", `SELECT count(*) FROM delivery_groups WHERE unresolved=1`, &e.DeliveriesUnresolved},
		{"artifact_reservations", `SELECT count(*) FROM artifact_reservations WHERE state='active'`, &e.ArtifactReservations},
		{"task_runs", `SELECT count(*) FROM task_runs WHERE execution_state IN ('running','cancel_requested','execution_unknown')`, &e.TaskRunsExecuting},
		{"task_attempts", `SELECT count(*) FROM task_attempts WHERE reservation_state='held'`, &e.TaskAttemptsHeld},
	}
	for _, p := range probes {
		ok, err := hasTable(ctx, db, p.table)
		if err != nil {
			return e, err
		}
		if !ok {
			continue
		}
		if err := db.QueryRowContext(ctx, p.query).Scan(p.dst); err != nil {
			return e, fmt.Errorf("store: work evidence (%s): %w", p.table, err)
		}
	}
	return e, nil
}
