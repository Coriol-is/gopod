package store

import (
	"context"
	"fmt"
)

func (s *Store) applyTopicDeletionSchema(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS topic_deletions (
 namespace_id TEXT NOT NULL,
 namespace_epoch INTEGER NOT NULL,
 thread_id INTEGER NOT NULL CHECK(thread_id>0),
 intent_id TEXT NOT NULL UNIQUE,
 registration_revision INTEGER NOT NULL,
 chat_jid TEXT NOT NULL,
 folder TEXT NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('deleting','uncertain','deleted','retired','refused')),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(namespace_id,namespace_epoch,thread_id))`)
	if err != nil {
		return fmt.Errorf("topic deletion schema: %w", err)
	}
	return nil
}
