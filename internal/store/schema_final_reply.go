package store

import (
	"context"
	"fmt"
)

// Final intents freeze presentation before any preparation. Ready plans contain
// exact payloads and artifact references; recovery never renders or synthesizes.
var finalReplySchema = []string{
	`CREATE TABLE IF NOT EXISTS final_reply_intents (
 result_id TEXT PRIMARY KEY REFERENCES turn_results(result_id),
 input_json BLOB NOT NULL,input_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('preparing','ready','published')),
 plan_json BLOB,plan_hash TEXT NOT NULL DEFAULT '',
 delivery_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL)`,
	`CREATE TRIGGER IF NOT EXISTS final_reply_input_immutable BEFORE UPDATE OF result_id,input_json,input_hash,created_at ON final_reply_intents
 BEGIN SELECT RAISE(ABORT,'final reply intent is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS final_reply_plan_immutable BEFORE UPDATE OF plan_json,plan_hash ON final_reply_intents
 WHEN OLD.state <> 'preparing' BEGIN SELECT RAISE(ABORT,'final reply plan is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS final_reply_transition BEFORE UPDATE OF state ON final_reply_intents
 WHEN NOT ((OLD.state='preparing' AND NEW.state='ready') OR (OLD.state='ready' AND NEW.state='published'))
 BEGIN SELECT RAISE(ABORT,'invalid final reply transition'); END`,
}

func (s *Store) applyFinalReplySchema(ctx context.Context) error {
	for _, stmt := range finalReplySchema {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("final reply schema: %w", err)
		}
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE outbox_parts ADD COLUMN reply_to_message_id INTEGER NOT NULL DEFAULT 0`); err != nil && !isDuplicateColumn(err) {
		return err
	}
	// Separate trigger upgrades old databases without changing their part hashes.
	_, err := s.db.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS outbox_reply_target_immutable BEFORE UPDATE OF reply_to_message_id ON outbox_parts BEGIN SELECT RAISE(ABORT,'outbox reply target is immutable'); END`)
	return err
}
