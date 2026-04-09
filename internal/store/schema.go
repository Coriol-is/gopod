package store

import (
	"context"
	"fmt"
)

// schemaStatements is the set of CREATE TABLE / CREATE INDEX /
// CREATE VIRTUAL TABLE / CREATE TRIGGER statements that picoclaw needs.
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
	// --- Telegram chats: every chat picoclaw has ever heard from.
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

	// --- Chats explicitly registered with picoclaw (have a chat folder).
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
	`CREATE TABLE IF NOT EXISTS sessions (
	  chat_folder TEXT PRIMARY KEY,
	  session_id TEXT NOT NULL,
	  updated_at INTEGER NOT NULL
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

	// --- Generic key/value state (router cursors etc.).
	`CREATE TABLE IF NOT EXISTS router_state (
	  key TEXT PRIMARY KEY,
	  value TEXT NOT NULL
	)`,

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

// applySchema runs every statement in schemaStatements in order. Idempotent.
func (s *Store) applySchema(ctx context.Context) error {
	for i, stmt := range schemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	return nil
}
