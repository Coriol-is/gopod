package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// Proactive presets (CP-033). Per-owner preset state lives in side tables so
// the trigger-guarded scheduled_tasks table gains no feature columns. The
// schema is additive and idempotent; an older binary ignores these tables.
var proactiveSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS proactive_presets (
 preset_id TEXT PRIMARY KEY,
 task_id TEXT NOT NULL DEFAULT '',
 settings TEXT NOT NULL DEFAULT '{}',
 template_version INTEGER NOT NULL DEFAULT 0,
 applied_hash TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL DEFAULT 0)`,
	`CREATE TABLE IF NOT EXISTS proactive_settings (
 key TEXT PRIMARY KEY,
 value TEXT NOT NULL)`,
}

func (s *Store) applyProactiveSchema(ctx context.Context) error {
	for i, stmt := range proactiveSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("proactive statement %d: %w", i, err)
		}
	}
	return nil
}

// ProactivePreset is one row of proactive_presets.
type ProactivePreset struct {
	PresetID        string
	TaskID          string // scheduled_tasks.id, "" when never enabled
	Settings        string // JSON owned by internal/proactive
	TemplateVersion int
	AppliedHash     string // sha256(prompt|schedule_value) gopod last wrote
	UpdatedAt       int64  // unix ms
}

// GetProactivePreset returns the row for a preset id; ok is false when absent.
func (s *Store) GetProactivePreset(ctx context.Context, id string) (p ProactivePreset, ok bool, err error) {
	err = s.taskDB(ctx).QueryRowContext(ctx,
		`SELECT preset_id, task_id, settings, template_version, applied_hash, updated_at FROM proactive_presets WHERE preset_id = ?`, id).
		Scan(&p.PresetID, &p.TaskID, &p.Settings, &p.TemplateVersion, &p.AppliedHash, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ProactivePreset{}, false, nil
	}
	if err != nil {
		return ProactivePreset{}, false, fmt.Errorf("store: GetProactivePreset: %w", err)
	}
	return p, true, nil
}

// PutProactivePreset inserts or replaces a preset row.
func (s *Store) PutProactivePreset(ctx context.Context, p ProactivePreset) error {
	if p.PresetID == "" {
		return errors.New("store: PutProactivePreset: empty PresetID")
	}
	if p.Settings == "" {
		p.Settings = "{}"
	}
	_, err := s.taskDB(ctx).ExecContext(ctx, `
		INSERT INTO proactive_presets (preset_id, task_id, settings, template_version, applied_hash, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(preset_id) DO UPDATE SET task_id=excluded.task_id, settings=excluded.settings,
		  template_version=excluded.template_version, applied_hash=excluded.applied_hash, updated_at=excluded.updated_at`,
		p.PresetID, p.TaskID, p.Settings, p.TemplateVersion, p.AppliedHash, p.UpdatedAt)
	if err != nil {
		return fmt.Errorf("store: PutProactivePreset: %w", err)
	}
	return nil
}

// ListProactivePresets returns all preset rows ordered by id.
func (s *Store) ListProactivePresets(ctx context.Context) ([]ProactivePreset, error) {
	rows, err := s.taskDB(ctx).QueryContext(ctx,
		`SELECT preset_id, task_id, settings, template_version, applied_hash, updated_at FROM proactive_presets ORDER BY preset_id`)
	if err != nil {
		return nil, fmt.Errorf("store: ListProactivePresets: %w", err)
	}
	defer rows.Close()
	var out []ProactivePreset
	for rows.Next() {
		var p ProactivePreset
		if err := rows.Scan(&p.PresetID, &p.TaskID, &p.Settings, &p.TemplateVersion, &p.AppliedHash, &p.UpdatedAt); err != nil {
			return nil, fmt.Errorf("store: ListProactivePresets: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// GetProactiveSetting reads one proactive_settings value; ok is false when unset.
func (s *Store) GetProactiveSetting(ctx context.Context, key string) (string, bool, error) {
	var v string
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT value FROM proactive_settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("store: GetProactiveSetting(%q): %w", key, err)
	}
	return v, true, nil
}

// SetProactiveSetting upserts a proactive_settings value.
func (s *Store) SetProactiveSetting(ctx context.Context, key, value string) error {
	_, err := s.taskDB(ctx).ExecContext(ctx, `
		INSERT INTO proactive_settings (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`, key, value)
	if err != nil {
		return fmt.Errorf("store: SetProactiveSetting(%q): %w", key, err)
	}
	return nil
}

// DeleteProactiveSetting removes a proactive_settings key (absent is not an error).
func (s *Store) DeleteProactiveSetting(ctx context.Context, key string) error {
	if _, err := s.taskDB(ctx).ExecContext(ctx, `DELETE FROM proactive_settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: DeleteProactiveSetting(%q): %w", key, err)
	}
	return nil
}

// InTaskTx runs fn in one transaction. Every Store task method and the
// proactive methods called with the context fn receives join that transaction,
// so a preset row and its scheduled task change atomically. Nested calls join
// the outer transaction through a savepoint.
func (s *Store) InTaskTx(ctx context.Context, fn func(ctx context.Context) error) error {
	_, err := s.withTx(ctx, func(q taskSQL) error {
		tx, ok := q.(*sql.Tx)
		if !ok {
			return errors.New("store: InTaskTx: no transaction")
		}
		return fn(context.WithValue(ctx, taskTransactionKey{}, taskTransaction{owner: s, tx: tx}))
	})
	return err
}
