package store

import (
	"context"
	"database/sql"
	"fmt"
)

// GetState returns a value from the router_state KV table.
// Returns "" if the key doesn't exist (not an error).
func (s *Store) GetState(ctx context.Context, key string) (string, error) {
	var value string
	err := s.db.QueryRowContext(ctx,
		`SELECT value FROM router_state WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("store: GetState(%q): %w", key, err)
	}
	return value, nil
}

// SetState upserts a value in the router_state KV table.
func (s *Store) SetState(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO router_state (key, value) VALUES (?, ?)
		ON CONFLICT(key) DO UPDATE SET value = excluded.value`,
		key, value)
	if err != nil {
		return fmt.Errorf("store: SetState(%q): %w", key, err)
	}
	return nil
}
