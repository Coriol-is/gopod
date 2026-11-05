package store

import (
	"context"
	"fmt"
)

func (s *Store) applyOrdinaryStopSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS ordinary_stop_intents(intent_id TEXT PRIMARY KEY,binding BLOB NOT NULL,created_at INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS ordinary_stop_members(intent_id TEXT NOT NULL REFERENCES ordinary_stop_intents(intent_id),turn_id INTEGER NOT NULL REFERENCES turns(id),attempt INTEGER NOT NULL CHECK(attempt>0),PRIMARY KEY(turn_id,attempt))`,
		`CREATE INDEX IF NOT EXISTS ordinary_stop_members_intent ON ordinary_stop_members(intent_id)`,
		`CREATE TABLE IF NOT EXISTS ordinary_stop_completions(intent_id TEXT PRIMARY KEY REFERENCES ordinary_stop_intents(intent_id),result_text TEXT NOT NULL,outcome TEXT NOT NULL CHECK(outcome IN ('returned','error','no_replay')),created_at INTEGER NOT NULL)`,
	}
	for _, table := range []string{"ordinary_stop_intents", "ordinary_stop_members", "ordinary_stop_completions"} {
		for _, op := range []string{"UPDATE", "DELETE"} {
			statements = append(statements, fmt.Sprintf("CREATE TRIGGER IF NOT EXISTS %s_no_%s BEFORE %s ON %s BEGIN SELECT RAISE(ABORT,'ordinary Stop evidence immutable'); END", table, op, op, table))
		}
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("ordinary Stop schema: %w", err)
		}
	}
	return nil
}
