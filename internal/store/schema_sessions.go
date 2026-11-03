package store

import (
	"context"
	"fmt"
)

// sessionSchemaStatements are the durable execution-session identity tables
// (CP-037 P1). They are additive: the legacy sessions table stays authoritative
// until the session_mapping marker in router_state flips to durable.
var sessionSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS execution_sessions (
 session_id TEXT PRIMARY KEY CHECK(session_id<>''),
 namespace_id TEXT NOT NULL,
 namespace_epoch INTEGER NOT NULL,
 registration_revision INTEGER NOT NULL,
 folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL,
 kind TEXT NOT NULL CHECK(kind IN ('conversation','task_attempt')),
 context_epoch INTEGER NOT NULL CHECK(context_epoch>=1),
 workspace_id TEXT NOT NULL,
 lifecycle TEXT NOT NULL CHECK(lifecycle IN ('active','archived','retired')),
 created_at INTEGER NOT NULL,
 archived_at INTEGER)`,
	`CREATE TABLE IF NOT EXISTS conversation_bindings (
 namespace_id TEXT NOT NULL,
 namespace_epoch INTEGER NOT NULL,
 thread_id INTEGER NOT NULL,
 active_session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT,
 revision INTEGER NOT NULL DEFAULT 1,
 lifecycle TEXT NOT NULL CHECK(lifecycle IN ('active','retired')),
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(namespace_id, namespace_epoch, thread_id))`,
	`CREATE TABLE IF NOT EXISTS native_contexts (
 session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT,
 context_epoch INTEGER NOT NULL,
 provider TEXT NOT NULL,
 native_id TEXT NOT NULL CHECK(native_id<>''),
 profile_revision TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL,
 PRIMARY KEY(session_id, context_epoch, provider))`,
	`CREATE TABLE IF NOT EXISTS attempt_bindings (
 turn_id INTEGER PRIMARY KEY REFERENCES turns(id) ON DELETE RESTRICT,
 session_id TEXT NOT NULL REFERENCES execution_sessions(session_id) ON DELETE RESTRICT,
 context_epoch INTEGER NOT NULL,
 provider TEXT NOT NULL DEFAULT '',
 model TEXT NOT NULL DEFAULT '',
 profile_fingerprint TEXT NOT NULL DEFAULT '',
 runtime_generation TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS execution_claims (
 session_id TEXT PRIMARY KEY REFERENCES execution_sessions(session_id) ON DELETE RESTRICT,
 turn_id INTEGER NOT NULL UNIQUE REFERENCES turns(id) ON DELETE RESTRICT,
 state TEXT NOT NULL CHECK(state IN ('held','execution_unknown')),
 claimed_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_execution_sessions_scope ON execution_sessions(namespace_id, namespace_epoch, thread_id)`,
}

func (s *Store) applySessionSchema(ctx context.Context) error {
	for i, stmt := range sessionSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("session schema statement %d: %w", i, err)
		}
	}
	return nil
}
