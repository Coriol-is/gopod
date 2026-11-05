package store

import (
	"context"
	"fmt"
)

// Final intents freeze presentation before any preparation. Ready plans contain
// exact payloads and artifact references; recovery never renders or synthesizes.
var finalReplySchema = []string{
	`CREATE TABLE IF NOT EXISTS outbox_code_plans (
 delivery_id TEXT PRIMARY KEY REFERENCES delivery_groups(delivery_id),
 manifest BLOB, manifest_hash TEXT NOT NULL, prefix_count INTEGER NOT NULL,
 selected_mask INTEGER, terminal_reason TEXT NOT NULL DEFAULT '')`,
	`CREATE TRIGGER IF NOT EXISTS outbox_code_identity_immutable BEFORE UPDATE OF delivery_id,manifest_hash,prefix_count ON outbox_code_plans BEGIN SELECT RAISE(ABORT,'code plan identity immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS outbox_code_manifest_immutable BEFORE UPDATE OF manifest ON outbox_code_plans WHEN NEW.manifest IS NOT NULL OR coalesce((SELECT unresolved FROM delivery_groups WHERE delivery_id=NEW.delivery_id),1)=1 BEGIN SELECT RAISE(ABORT,'code manifest immutable until resolved'); END`,
	`CREATE TRIGGER IF NOT EXISTS outbox_code_selection_immutable BEFORE UPDATE OF selected_mask ON outbox_code_plans WHEN OLD.selected_mask IS NOT NULL OR OLD.terminal_reason<>'' BEGIN SELECT RAISE(ABORT,'code selection immutable'); END`,
	`CREATE TABLE IF NOT EXISTS final_reply_intents (
 result_id TEXT PRIMARY KEY REFERENCES turn_results(result_id),
 input_json BLOB NOT NULL,input_hash TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('preparing','ready','published')),
 plan_json BLOB,plan_hash TEXT NOT NULL DEFAULT '',
 delivery_id TEXT NOT NULL DEFAULT '',created_at INTEGER NOT NULL)`,
	`CREATE TRIGGER IF NOT EXISTS final_reply_input_immutable BEFORE UPDATE OF result_id,input_json,input_hash,created_at ON final_reply_intents
 BEGIN SELECT RAISE(ABORT,'final reply intent is immutable'); END`,
	`DROP TRIGGER IF EXISTS final_reply_plan_immutable`,
	`CREATE TRIGGER final_reply_plan_immutable BEFORE UPDATE OF plan_json,plan_hash ON final_reply_intents
 WHEN OLD.state <> 'preparing' AND NOT (OLD.state='published' AND NEW.plan_json IS NULL AND NEW.plan_hash=OLD.plan_hash AND coalesce((SELECT unresolved FROM delivery_groups WHERE delivery_id=OLD.delivery_id),1)=0)
 BEGIN SELECT RAISE(ABORT,'final reply plan is immutable'); END`,
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
