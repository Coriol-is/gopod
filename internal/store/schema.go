package store

import (
	"context"
	"fmt"
	"strings"
)

// schemaStatements is the set of CREATE TABLE / CREATE INDEX /
// CREATE VIRTUAL TABLE / CREATE TRIGGER statements that gopod needs.
//
// All statements use IF NOT EXISTS so applySchema is idempotent. The
// schema mirrors:
//   - docs/ARCHITECTURE.md §4.3 (relational tables)
//   - docs/MEMORY.md §1 Layer 3 (memory tables, including the
//     sqlite-vec vec0 virtual table)
//
// The vector dimension is hard-coded to 1024 per ADR D016
// (text-embedding-3-small at server-side dimensions=1024).
//
// When the schema needs to evolve post-M0, introduce a schema_version
// row in router_state and a migrations runner here. For now everything
// is additive and IF NOT EXISTS keeps re-runs safe.
var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS namespace_ownership (
 folder TEXT PRIMARY KEY, namespace_id TEXT NOT NULL, chat_jid TEXT NOT NULL,
 namespace_epoch INTEGER NOT NULL, retired INTEGER NOT NULL DEFAULT 0,
 created_at INTEGER NOT NULL, retired_at INTEGER
 )`,
	`CREATE TABLE IF NOT EXISTS ipc_task_requests (
 source_folder TEXT NOT NULL,request_file TEXT NOT NULL,payload_hash TEXT NOT NULL,
 result_json BLOB NOT NULL,created_at INTEGER NOT NULL, archived INTEGER NOT NULL DEFAULT 0,
 PRIMARY KEY(source_folder,request_file))`,
	`CREATE TABLE IF NOT EXISTS conversation_history (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 chat_folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL,
 role TEXT NOT NULL CHECK(role IN ('user','assistant')),
 content TEXT NOT NULL
 )`,
	`CREATE INDEX IF NOT EXISTS idx_conversation_history ON conversation_history(chat_folder,thread_id,id)`,
	// --- Telegram chats: every chat gopod has ever heard from.
	`CREATE TABLE IF NOT EXISTS chats (
	  jid TEXT PRIMARY KEY,
	  name TEXT,
	  last_message_time INTEGER,
	  is_group INTEGER NOT NULL DEFAULT 0
	)`,

	// --- All inbound + outbound message history.
	`CREATE TABLE IF NOT EXISTS messages (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  chat_jid TEXT NOT NULL,
	  tg_message_id INTEGER NOT NULL,
	  sender TEXT NOT NULL,
	  sender_name TEXT,
	  content TEXT NOT NULL,
	  timestamp INTEGER NOT NULL,
	  is_from_me INTEGER NOT NULL DEFAULT 0,
	  is_bot_message INTEGER NOT NULL DEFAULT 0,
	  reply_to_tg_message_id INTEGER,
	  reply_to_content TEXT,
	  reply_to_sender_name TEXT,
	  UNIQUE(chat_jid, tg_message_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_messages_chat_ts
	   ON messages(chat_jid, timestamp)`,

	// --- Chats explicitly registered with gopod (have a chat folder).
	`CREATE TABLE IF NOT EXISTS registered_chats (
	  jid TEXT PRIMARY KEY,
	  name TEXT,
	  folder TEXT NOT NULL UNIQUE,
	  trigger_pattern TEXT,
	  requires_trigger INTEGER NOT NULL DEFAULT 1,
	  is_owner INTEGER NOT NULL DEFAULT 0,
	  added_at INTEGER NOT NULL
	)`,

	// --- Claude CLI session continuity per chat.
	// Keyed by (chat, thread, provider); legacy single-key tables are
	// rebuilt by migrateSessions. Fresh DBs get the new shape directly.
	`CREATE TABLE IF NOT EXISTS sessions (
	  chat_folder TEXT NOT NULL,
	  thread_id   INTEGER NOT NULL DEFAULT 0,
	  provider    TEXT NOT NULL DEFAULT 'claude',
	  session_id  TEXT NOT NULL,
	  updated_at  INTEGER NOT NULL,
	  PRIMARY KEY (chat_folder, thread_id, provider)
	)`,

	// --- Scheduler state (M4).
	`CREATE TABLE IF NOT EXISTS scheduled_tasks (
	  id TEXT PRIMARY KEY,
	  chat_folder TEXT NOT NULL,
	  chat_jid TEXT NOT NULL,
	  prompt TEXT NOT NULL,
	  schedule_type TEXT NOT NULL,
	  schedule_value TEXT NOT NULL,
	  next_run INTEGER,
	  last_run INTEGER,
	  status TEXT NOT NULL DEFAULT 'active',
	  created_at INTEGER NOT NULL
	)`,
	`CREATE INDEX IF NOT EXISTS idx_tasks_due
	   ON scheduled_tasks(status, next_run)`,
	`CREATE TABLE IF NOT EXISTS task_run_logs (
	  task_id TEXT NOT NULL,
	  run_at INTEGER NOT NULL,
	  duration_ms INTEGER,
	  status TEXT NOT NULL,
	  result TEXT,
	  error TEXT
	)`,

	// --- Structured logs (M8b).
	`CREATE TABLE IF NOT EXISTS logs (
	  id INTEGER PRIMARY KEY AUTOINCREMENT,
	  timestamp INTEGER NOT NULL,
	  level TEXT NOT NULL,
	  subsystem TEXT,
	  message TEXT NOT NULL,
	  attrs_json TEXT
	)`,
	`CREATE INDEX IF NOT EXISTS idx_logs_ts
	   ON logs(timestamp DESC)`,

	// --- Generic key/value state (router cursors etc.).
	`CREATE TABLE IF NOT EXISTS router_state (
	  key TEXT PRIMARY KEY,
	  value TEXT NOT NULL
	)`,

	// --- Durable agent turns (docs/superpowers/specs/2026-10-03-durable-turns-design.md).
	// source_id is the per-source dedup key: "<tg_chat_id>:<tg_message_id>"
	// for telegram (message ids are unique only within a chat),
	// "<task_id>:<next_run>" for task.
	`CREATE TABLE IF NOT EXISTS turns (
	  id                 INTEGER PRIMARY KEY AUTOINCREMENT,
	  source             TEXT NOT NULL,
	  source_id          TEXT NOT NULL,
	  chat_folder        TEXT NOT NULL,
	  chat_id            INTEGER NOT NULL,
	  tg_message_id      INTEGER NOT NULL DEFAULT 0,
	  is_owner           INTEGER NOT NULL DEFAULT 0,
	  is_voice           INTEGER NOT NULL DEFAULT 0,
	  text               TEXT NOT NULL,
	  file_path          TEXT,
	  status             TEXT NOT NULL,
	  attempts           INTEGER NOT NULL DEFAULT 0,
	  placeholder_msg_id INTEGER NOT NULL DEFAULT 0,
	  reply_msg_id       INTEGER NOT NULL DEFAULT 0,
	  error              TEXT,
	  created_at         INTEGER NOT NULL,
	  started_at         INTEGER,
	  finished_at        INTEGER,
	  UNIQUE(source, source_id)
	)`,
	`CREATE INDEX IF NOT EXISTS idx_turns_status ON turns(status)`,

	// --- Memory layer 3: relational metadata.
	`CREATE TABLE IF NOT EXISTS memories (
	  id            INTEGER PRIMARY KEY AUTOINCREMENT,
	  chat_folder   TEXT NOT NULL,
	  kind          TEXT NOT NULL,
	  source        TEXT,
	  title         TEXT,
	  content       TEXT NOT NULL,
	  ref_chat_jid  TEXT,
	  ref_msg_id    INTEGER,
	  meta_json     TEXT,
	  embed_model   TEXT NOT NULL,
	  created_at    INTEGER NOT NULL,
	  updated_at    INTEGER NOT NULL,
	  pinned        INTEGER NOT NULL DEFAULT 0
	)`,
	`CREATE INDEX IF NOT EXISTS idx_mem_chat_kind
	   ON memories(chat_folder, kind, created_at)`,

	// --- Memory layer 3: dense vector index.
	// vec0 is provided by the sqlite-vec extension. Dimension is fixed
	// per D016 at 1024 for text-embedding-3-small.
	`CREATE VIRTUAL TABLE IF NOT EXISTS memory_vec USING vec0(
	  embedding float[1024]
	)`,

	// --- Memory layer 3: lexical FTS for hybrid search.
	`CREATE VIRTUAL TABLE IF NOT EXISTS memory_fts USING fts5(
	  content,
	  content='memories',
	  content_rowid='id',
	  tokenize='unicode61 remove_diacritics 2'
	)`,

	`CREATE TRIGGER IF NOT EXISTS mem_ai AFTER INSERT ON memories BEGIN
	  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS mem_ad AFTER DELETE ON memories BEGIN
	  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES('delete', old.id, old.content);
	END`,
	`CREATE TRIGGER IF NOT EXISTS mem_au AFTER UPDATE ON memories BEGIN
	  INSERT INTO memory_fts(memory_fts, rowid, content) VALUES('delete', old.id, old.content);
	  INSERT INTO memory_fts(rowid, content) VALUES (new.id, new.content);
	END`,
}

// migrations are ALTER TABLE statements that add columns to existing
// tables. SQLite doesn't support IF NOT EXISTS for columns, so we
// run each one and ignore "duplicate column name" errors.
var migrations = []string{
	`ALTER TABLE registered_chats ADD COLUMN namespace_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE registered_chats ADD COLUMN namespace_epoch INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE registered_chats ADD COLUMN registration_revision INTEGER NOT NULL DEFAULT 1`,
	`ALTER TABLE registered_chats ADD COLUMN group_epoch_ready INTEGER NOT NULL DEFAULT 0`,
	// Phase C: memory lifecycle fields.
	`ALTER TABLE memories ADD COLUMN status TEXT DEFAULT 'active'`,
	`ALTER TABLE memories ADD COLUMN superseded_by INTEGER`,
	`ALTER TABLE memories ADD COLUMN last_retrieved_at INTEGER`,
	// Durable turns: link task run logs to the turn that ran them.
	`ALTER TABLE task_run_logs ADD COLUMN turn_id INTEGER`,
	// Per-conversation sessions: turns carry the Telegram thread.
	`ALTER TABLE turns ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0`,
	// Per-conversation memory: the summary slot is scoped by thread.
	`ALTER TABLE memories ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0`,
}

// applySchema runs every statement in schemaStatements in order. Idempotent.
func (s *Store) applySchema(ctx context.Context) error {
	for i, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	if err := s.migrateSessions(ctx); err != nil {
		return err
	}
	// Run migrations (ALTER TABLE). Ignore "duplicate column" errors
	// so re-applying on an already-migrated DB is a no-op.
	for _, stmt := range migrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			if !isDuplicateColumn(err) {
				return fmt.Errorf("migration: %w\n--- sql ---\n%s", err, stmt)
			}
		}
	}
	if err := s.initializeNamespaceMetadata(ctx); err != nil {
		return err
	}
	if err := s.applyDeliverySchema(ctx); err != nil {
		return err
	}
	if err := s.applyArtifactSchema(ctx); err != nil {
		return err
	}
	if err := s.applyGroupTriggerSchema(ctx); err != nil {
		return err
	}
	return s.applyTaskSchema(ctx)
}

func isDuplicateColumn(err error) bool {
	return err != nil && strings.Contains(err.Error(), "duplicate column name")
}
