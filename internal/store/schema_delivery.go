package store

import (
	"context"
	"fmt"
)

// deliverySchemaStatements are the CP-002 result, delivery and durable IPC
// identity tables (docs/superpowers/specs/2026-10-26-telegram-outbox.md).
// Everything is additive and IF NOT EXISTS; no existing table is altered. The
// scope columns copy the ScopeRef snapshot taken at admission; identity itself
// stays in registered_chats/namespace_ownership.
//
// Immutability is enforced by triggers so that a later bug in any caller
// cannot rewrite a frozen result, a plan, or a request tombstone.
var deliverySchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS turn_results (
 result_id TEXT PRIMARY KEY,
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL, registration_revision INTEGER NOT NULL,
 chat_jid TEXT NOT NULL, chat_folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL CHECK(thread_id >= 0),
 visibility TEXT NOT NULL CHECK(visibility IN ('conversation','private')),
 execution_kind TEXT NOT NULL CHECK(execution_kind IN ('interactive','isolated_task')),
 provider TEXT NOT NULL CHECK(provider <> ''),
 model TEXT NOT NULL DEFAULT '', profile_fingerprint TEXT NOT NULL DEFAULT '',
 result_text TEXT NOT NULL, text_hash TEXT NOT NULL,
 authority TEXT NOT NULL DEFAULT 'model_reported' CHECK(authority = 'model_reported'),
 created_at INTEGER NOT NULL)`,
	`CREATE TABLE IF NOT EXISTS turn_result_members (
 result_id TEXT NOT NULL REFERENCES turn_results(result_id),
 turn_id INTEGER NOT NULL REFERENCES turns(id),
 attempt INTEGER NOT NULL CHECK(attempt > 0),
 PRIMARY KEY(result_id, turn_id, attempt),
 UNIQUE(turn_id, attempt))`,
	`CREATE TRIGGER IF NOT EXISTS turn_results_immutable_update BEFORE UPDATE ON turn_results
 BEGIN SELECT RAISE(ABORT, 'turn_results rows are immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS turn_results_immutable_delete BEFORE DELETE ON turn_results
 BEGIN SELECT RAISE(ABORT, 'turn_results rows are immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS turn_result_members_immutable_update BEFORE UPDATE ON turn_result_members
 BEGIN SELECT RAISE(ABORT, 'turn_result_members rows are immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS turn_result_members_immutable_delete BEFORE DELETE ON turn_result_members
 BEGIN SELECT RAISE(ABORT, 'turn_result_members rows are immutable'); END`,

	// Per-conversation ordering key: namespace/epoch/thread/visibility.
	`CREATE TABLE IF NOT EXISTS conversation_sequences (
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL,
 thread_id INTEGER NOT NULL, visibility TEXT NOT NULL,
 last_seq INTEGER NOT NULL,
 PRIMARY KEY(namespace_id, namespace_epoch, thread_id, visibility))`,

	`CREATE TABLE IF NOT EXISTS delivery_groups (
 delivery_id TEXT PRIMARY KEY,
 result_id TEXT REFERENCES turn_results(result_id),
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL, registration_revision INTEGER NOT NULL,
 chat_jid TEXT NOT NULL, chat_folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL CHECK(thread_id >= 0),
 visibility TEXT NOT NULL CHECK(visibility IN ('conversation','private')),
 actor_ref TEXT NOT NULL DEFAULT '', task_ref TEXT NOT NULL DEFAULT '', run_ref TEXT NOT NULL DEFAULT '',
 delivery_owner TEXT NOT NULL CHECK(delivery_owner IN ('legacy_agent_ipc','controller_result')),
 conversation_seq INTEGER NOT NULL CHECK(conversation_seq > 0),
 expires_at INTEGER NOT NULL,
 plan_hash TEXT NOT NULL, part_count INTEGER NOT NULL CHECK(part_count BETWEEN 1 AND 32),
 state TEXT NOT NULL CHECK(state IN ('pending','sending','partial','sent','uncertain','failed','cancelled','expired')),
 unresolved INTEGER NOT NULL CHECK(unresolved IN (0,1)),
 created_at INTEGER NOT NULL, updated_at INTEGER NOT NULL, compacted_at INTEGER,
 UNIQUE(namespace_id, namespace_epoch, thread_id, visibility, conversation_seq),
 CHECK(result_id IS NULL OR delivery_owner = 'controller_result'))`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_delivery_groups_result ON delivery_groups(result_id) WHERE result_id IS NOT NULL`,
	`CREATE INDEX IF NOT EXISTS idx_delivery_groups_unresolved ON delivery_groups(namespace_id) WHERE unresolved = 1`,
	`CREATE INDEX IF NOT EXISTS idx_delivery_groups_terminal ON delivery_groups(updated_at) WHERE unresolved = 0 AND compacted_at IS NULL`,
	`CREATE TRIGGER IF NOT EXISTS delivery_groups_immutable BEFORE UPDATE OF
 delivery_id, result_id, namespace_id, namespace_epoch, registration_revision, chat_jid, chat_folder,
 thread_id, visibility, actor_ref, task_ref, run_ref, delivery_owner, conversation_seq, expires_at,
 plan_hash, part_count, created_at ON delivery_groups
 BEGIN SELECT RAISE(ABORT, 'delivery plan and identity are immutable'); END`,

	`CREATE TABLE IF NOT EXISTS outbox_parts (
 part_id TEXT PRIMARY KEY,
 delivery_id TEXT NOT NULL REFERENCES delivery_groups(delivery_id),
 ordinal INTEGER NOT NULL CHECK(ordinal >= 0 AND ordinal < 32),
 kind TEXT NOT NULL CHECK(kind IN ('text','edit','upload')),
 payload BLOB, part_hash TEXT NOT NULL,
 artifact_ref TEXT NOT NULL DEFAULT '',
 parse_mode TEXT NOT NULL DEFAULT '', reply_markup TEXT NOT NULL DEFAULT '',
 target_message_id INTEGER NOT NULL DEFAULT 0,
 state TEXT NOT NULL CHECK(state IN ('pending','sending','sent','uncertain','failed','expired','cancelled')),
 attempts INTEGER NOT NULL DEFAULT 0,
 next_attempt_at INTEGER NOT NULL,
 remote_message_id INTEGER NOT NULL DEFAULT 0,
 reason_code TEXT NOT NULL DEFAULT '',
 updated_at INTEGER NOT NULL,
 UNIQUE(delivery_id, ordinal))`,
	`CREATE INDEX IF NOT EXISTS idx_outbox_parts_due ON outbox_parts(state, next_attempt_at)`,
	`CREATE TRIGGER IF NOT EXISTS outbox_parts_immutable BEFORE UPDATE OF
 part_id, delivery_id, ordinal, kind, part_hash, artifact_ref, parse_mode, reply_markup, target_message_id ON outbox_parts
 BEGIN SELECT RAISE(ABORT, 'outbox part plan is immutable'); END`,
	// The payload may only be cleared (terminal compaction), and only once the
	// owning group is resolved; unresolved evidence keeps its exact bytes.
	`DROP TRIGGER IF EXISTS outbox_parts_payload_immutable`,
	`CREATE TRIGGER outbox_parts_payload_immutable BEFORE UPDATE OF payload ON outbox_parts
 WHEN NEW.payload IS NOT NULL OR coalesce((SELECT unresolved FROM delivery_groups WHERE delivery_id=NEW.delivery_id),1) = 1
 BEGIN SELECT RAISE(ABORT, 'outbox part payload is immutable until the delivery is resolved'); END`,

	`CREATE TABLE IF NOT EXISTS outbox_attempts (
 attempt_id INTEGER PRIMARY KEY AUTOINCREMENT,
 part_id TEXT NOT NULL REFERENCES outbox_parts(part_id),
 fence TEXT NOT NULL,
 started_at INTEGER NOT NULL, ended_at INTEGER,
 outcome TEXT NOT NULL DEFAULT '',
 diagnostic TEXT NOT NULL DEFAULT '')`,
	`CREATE INDEX IF NOT EXISTS idx_outbox_attempts_part ON outbox_attempts(part_id)`,

	// Durable IPC identity and dedupe tombstone. Rows are never rewritten and are
	// kept without payload after delivery compaction.
	`CREATE TABLE IF NOT EXISTS ipc_message_requests (
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL,
 request_key TEXT NOT NULL CHECK(request_key <> ''),
 payload_hash TEXT NOT NULL,
 delivery_id TEXT REFERENCES delivery_groups(delivery_id),
 outcome TEXT NOT NULL CHECK(outcome IN ('accepted','rejected')),
 reason_code TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 PRIMARY KEY(namespace_id, namespace_epoch, request_key),
 CHECK((outcome = 'accepted' AND delivery_id IS NOT NULL) OR (outcome = 'rejected' AND delivery_id IS NULL)))`,
	`CREATE TRIGGER IF NOT EXISTS ipc_message_requests_immutable BEFORE UPDATE ON ipc_message_requests
 BEGIN SELECT RAISE(ABORT, 'ipc request tombstones are immutable'); END`,
}

func (s *Store) applyDeliverySchema(ctx context.Context) error {
	for i, stmt := range append(deliverySchemaStatements, plainFallbackSchema) {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("delivery statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	return nil
}
