package store

import (
	"context"
	"fmt"
)

// Task reliability schema (CP-004 / CP-021 / CP-022, plan PR 1). Everything is
// additive: scheduled_tasks gains columns whose defaults describe the legacy
// behavior (execution_version 0, legacy_main, legacy_ipc, thread 0, no
// ScopeRef), and four ledger tables are created. No existing row is rewritten
// and nothing is fabricated for old rows: legacy tasks have no scope snapshot,
// no outcome and no occurrence history.
//
// Managed evidence is never cascaded or deleted by the store. Foreign keys are
// ON DELETE RESTRICT and BEFORE DELETE triggers reject raw deletion of managed
// definitions and every ledger row; a future explicit maintenance step must
// replace these guards deliberately (see TaskPurgeBlockers).

// taskColumnMigrations extend scheduled_tasks. A duplicate-column error means
// the migration already ran (restart-idempotent, same as schema.go).
var taskColumnMigrations = []string{
	`ALTER TABLE scheduled_tasks ADD COLUMN execution_version INTEGER NOT NULL DEFAULT 0 CHECK(execution_version IN (0,1))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN definition_revision INTEGER NOT NULL DEFAULT 1 CHECK(definition_revision >= 1)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN kind TEXT NOT NULL DEFAULT 'agent' CHECK(kind IN ('agent','reminder'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN session_mode TEXT NOT NULL DEFAULT 'legacy_main' CHECK(session_mode IN ('legacy_main','isolated','none'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN delivery_mode TEXT NOT NULL DEFAULT 'legacy_ipc' CHECK(delivery_mode IN ('legacy_ipc','controller_result'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN namespace_id TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN namespace_epoch INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE scheduled_tasks ADD COLUMN registration_revision INTEGER NOT NULL DEFAULT 0`,
	`ALTER TABLE scheduled_tasks ADD COLUMN thread_id INTEGER NOT NULL DEFAULT 0 CHECK(thread_id >= 0)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN visibility TEXT NOT NULL DEFAULT '' CHECK(visibility IN ('','conversation','private'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN actor_ref TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN literal_text TEXT`,
	`ALTER TABLE scheduled_tasks ADD COLUMN timezone TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN overlap_policy TEXT NOT NULL DEFAULT '' CHECK(overlap_policy IN ('','skip','coalesce','queue'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN misfire_policy TEXT NOT NULL DEFAULT '' CHECK(misfire_policy IN ('','skip','coalesce','catch_up'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN misfire_grace_ms INTEGER NOT NULL DEFAULT 0 CHECK(misfire_grace_ms >= 0)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN queue_limit INTEGER NOT NULL DEFAULT 0 CHECK(queue_limit BETWEEN 0 AND 10)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN catch_up_limit INTEGER NOT NULL DEFAULT 0 CHECK(catch_up_limit BETWEEN 0 AND 10)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN dst_fold_policy TEXT NOT NULL DEFAULT '' CHECK(dst_fold_policy IN ('','once','both'))`,
	`ALTER TABLE scheduled_tasks ADD COLUMN expires_at INTEGER`,
	`ALTER TABLE scheduled_tasks ADD COLUMN max_runs INTEGER CHECK(max_runs IS NULL OR max_runs >= 1)`,
	`ALTER TABLE scheduled_tasks ADD COLUMN objective TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN done_condition TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN terminal_reason TEXT NOT NULL DEFAULT ''`,
	`ALTER TABLE scheduled_tasks ADD COLUMN deleted_at INTEGER`,
	`ALTER TABLE scheduled_tasks ADD COLUMN deletion_reason TEXT NOT NULL DEFAULT ''`,
}

// managedShape is the DB-side mirror of strict kind validation (Go validates
// first and gives precise errors; this keeps a buggy writer out).
const managedShape = `(
 ((NEW.kind='agent' AND NEW.prompt<>'' AND NEW.literal_text IS NULL AND NEW.session_mode='isolated')
  OR (NEW.kind='reminder' AND NEW.prompt='' AND NEW.literal_text IS NOT NULL
      AND length(CAST(NEW.literal_text AS BLOB)) BETWEEN 1 AND 3500
      AND NEW.session_mode='none' AND NEW.delivery_mode='controller_result'))
 AND NEW.namespace_id<>'' AND NEW.namespace_epoch>0 AND NEW.registration_revision>0
 AND NEW.visibility<>'' AND NEW.actor_ref<>'' AND NEW.timezone<>''
 AND NEW.overlap_policy<>'' AND NEW.misfire_policy<>'' AND NEW.dst_fold_policy<>''
 AND NEW.queue_limit>=1 AND NEW.catch_up_limit>=1)`

// legacyShape is the shape every execution_version 0 row must keep.
const legacyShape = `(NEW.kind='agent' AND NEW.literal_text IS NULL AND NEW.session_mode='legacy_main'
 AND NEW.delivery_mode='legacy_ipc' AND NEW.namespace_id='' AND NEW.namespace_epoch=0
 AND NEW.registration_revision=0 AND NEW.thread_id=0 AND NEW.visibility='' AND NEW.deleted_at IS NULL)`

const terminalStatesSQL = `'completed','failed','cancelled','interrupted'`

var taskSchemaStatements = []string{
	`CREATE INDEX IF NOT EXISTS idx_tasks_managed_due ON scheduled_tasks(execution_version, status, next_run)`,

	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_managed_insert BEFORE INSERT ON scheduled_tasks
 WHEN NEW.execution_version=1 AND NOT ` + managedShape + `
 BEGIN SELECT RAISE(ABORT, 'managed task definition has an invalid shape'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_managed_update BEFORE UPDATE ON scheduled_tasks
 WHEN NEW.execution_version=1 AND NOT ` + managedShape + `
 BEGIN SELECT RAISE(ABORT, 'managed task definition has an invalid shape'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_legacy_insert BEFORE INSERT ON scheduled_tasks
 WHEN NEW.execution_version=0 AND NOT ` + legacyShape + `
 BEGIN SELECT RAISE(ABORT, 'legacy task rows keep legacy_main, legacy_ipc and thread 0'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_legacy_update BEFORE UPDATE ON scheduled_tasks
 WHEN NEW.execution_version=0 AND NOT ` + legacyShape + `
 BEGIN SELECT RAISE(ABORT, 'legacy task rows keep legacy_main, legacy_ipc and thread 0'); END`,
	// Identity is fixed for life. registration_revision may move only through
	// the explicit rebind API; switching a legacy row to managed needs a
	// separate approved migration that does not exist in this slice.
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_identity_immutable BEFORE UPDATE OF
 execution_version, kind, session_mode, namespace_id, namespace_epoch, thread_id, visibility, actor_ref, chat_folder, chat_jid
 ON scheduled_tasks
 WHEN NEW.execution_version<>OLD.execution_version OR (OLD.execution_version=1 AND (
  NEW.kind<>OLD.kind OR NEW.session_mode<>OLD.session_mode OR NEW.namespace_id<>OLD.namespace_id
  OR NEW.namespace_epoch<>OLD.namespace_epoch OR NEW.thread_id<>OLD.thread_id OR NEW.visibility<>OLD.visibility
  OR NEW.actor_ref<>OLD.actor_ref OR NEW.chat_folder<>OLD.chat_folder OR NEW.chat_jid<>OLD.chat_jid))
 BEGIN SELECT RAISE(ABORT, 'task identity is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_revision_step BEFORE UPDATE OF definition_revision ON scheduled_tasks
 WHEN NEW.definition_revision<>OLD.definition_revision AND NEW.definition_revision<>OLD.definition_revision+1
 BEGIN SELECT RAISE(ABORT, 'definition revision moves one step at a time'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_managed_delete BEFORE DELETE ON scheduled_tasks
 WHEN OLD.execution_version=1
 BEGIN SELECT RAISE(ABORT, 'managed task definitions are tombstoned, never deleted'); END`,
	`CREATE TRIGGER IF NOT EXISTS scheduled_tasks_tombstone_final BEFORE UPDATE ON scheduled_tasks
 WHEN OLD.deleted_at IS NOT NULL AND (NEW.deleted_at IS NULL OR NEW.deleted_at<>OLD.deleted_at OR NEW.status<>'done' OR NEW.next_run IS NOT 0)
 BEGIN SELECT RAISE(ABORT, 'a tombstoned task cannot be revived'); END`,

	`CREATE TABLE IF NOT EXISTS task_runs (
 run_id TEXT PRIMARY KEY CHECK(run_id <> ''),
 task_id TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE RESTRICT,
 definition_revision INTEGER NOT NULL CHECK(definition_revision >= 1),
 nominal_at INTEGER NOT NULL,
 namespace_id TEXT NOT NULL CHECK(namespace_id <> ''), namespace_epoch INTEGER NOT NULL CHECK(namespace_epoch > 0),
 registration_revision INTEGER NOT NULL CHECK(registration_revision > 0),
 chat_jid TEXT NOT NULL, chat_folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL CHECK(thread_id >= 0),
 visibility TEXT NOT NULL CHECK(visibility IN ('conversation','private')),
 actor_ref TEXT NOT NULL CHECK(actor_ref <> ''),
 trigger_kind TEXT NOT NULL CHECK(trigger_kind IN ('schedule')),
 session_kind TEXT NOT NULL CHECK(session_kind IN ('isolated')),
 delivery_mode TEXT NOT NULL CHECK(delivery_mode IN ('legacy_ipc','controller_result')),
 execution_state TEXT NOT NULL CHECK(execution_state IN ('queued','running','cancel_requested','completed','failed','cancelled','interrupted','execution_unknown')),
 publication_state TEXT NOT NULL DEFAULT 'not_requested' CHECK(publication_state IN ('not_requested','pending','published','failed')),
 result_id TEXT REFERENCES turn_results(result_id) ON DELETE RESTRICT,
 goal_status TEXT NOT NULL DEFAULT 'unknown' CHECK(goal_status IN ('unknown','achieved','partial','blocked')),
 goal_authority TEXT NOT NULL DEFAULT 'none' CHECK(goal_authority IN ('none','model_reported')),
 outcome_json TEXT CHECK(outcome_json IS NULL OR length(CAST(outcome_json AS BLOB)) <= 32768),
 checkpoint_json TEXT CHECK(checkpoint_json IS NULL OR length(CAST(checkpoint_json AS BLOB)) <= 32768),
 effects_state TEXT NOT NULL DEFAULT 'none' CHECK(effects_state IN ('none','unresolved','reconciled')),
 effects_evidence TEXT NOT NULL DEFAULT '' CHECK(length(effects_evidence) <= 512),
 stop_requested_at INTEGER, stop_reason TEXT NOT NULL DEFAULT '',
 failure_code TEXT NOT NULL DEFAULT '' CHECK(length(failure_code) <= 64),
 created_at INTEGER NOT NULL, settled_at INTEGER)`,
	`CREATE INDEX IF NOT EXISTS idx_task_runs_task ON task_runs(task_id, execution_state)`,

	`CREATE TABLE IF NOT EXISTS task_attempts (
 attempt_id TEXT PRIMARY KEY CHECK(attempt_id <> ''),
 run_id TEXT NOT NULL REFERENCES task_runs(run_id) ON DELETE RESTRICT,
 attempt_no INTEGER NOT NULL CHECK(attempt_no > 0),
 turn_id INTEGER NOT NULL UNIQUE REFERENCES turns(id) ON DELETE RESTRICT,
 execution_state TEXT NOT NULL CHECK(execution_state IN ('queued','reserved','running','cancel_requested','completed','failed','cancelled','interrupted','execution_unknown')),
 provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', profile_fingerprint TEXT NOT NULL DEFAULT '',
 native_session_uuid TEXT NOT NULL DEFAULT '',
 reservation_state TEXT NOT NULL DEFAULT 'none' CHECK(reservation_state IN ('none','held','released')),
 started_at INTEGER, exited_at INTEGER,
 exit_evidence TEXT NOT NULL DEFAULT '' CHECK(length(exit_evidence) <= 512),
 failure_code TEXT NOT NULL DEFAULT '' CHECK(length(failure_code) <= 64),
 created_at INTEGER NOT NULL,
 UNIQUE(run_id, attempt_no))`,
	`CREATE INDEX IF NOT EXISTS idx_task_attempts_held ON task_attempts(reservation_state) WHERE reservation_state = 'held'`,

	`CREATE TABLE IF NOT EXISTS task_occurrences (
 occurrence_id TEXT PRIMARY KEY CHECK(occurrence_id <> ''),
 task_id TEXT NOT NULL REFERENCES scheduled_tasks(id) ON DELETE RESTRICT,
 definition_revision INTEGER NOT NULL CHECK(definition_revision >= 1),
 nominal_at INTEGER NOT NULL,
 wall_slot_key TEXT NOT NULL DEFAULT '',
 namespace_id TEXT NOT NULL CHECK(namespace_id <> ''), namespace_epoch INTEGER NOT NULL CHECK(namespace_epoch > 0),
 registration_revision INTEGER NOT NULL CHECK(registration_revision > 0),
 chat_jid TEXT NOT NULL, chat_folder TEXT NOT NULL,
 thread_id INTEGER NOT NULL CHECK(thread_id >= 0),
 visibility TEXT NOT NULL CHECK(visibility IN ('conversation','private')),
 decision TEXT NOT NULL CHECK(decision IN ('admitted','skipped','coalesced','expired')),
 reason TEXT NOT NULL CHECK(reason <> '' AND length(reason) <= 64),
 run_id TEXT REFERENCES task_runs(run_id) ON DELETE RESTRICT,
 delivery_id TEXT REFERENCES delivery_groups(delivery_id) ON DELETE RESTRICT,
 created_at INTEGER NOT NULL,
 UNIQUE(task_id, definition_revision, nominal_at),
 CHECK(decision = 'admitted' OR (run_id IS NULL AND delivery_id IS NULL)),
 CHECK(run_id IS NULL OR delivery_id IS NULL))`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_occurrences_run ON task_occurrences(run_id) WHERE run_id IS NOT NULL`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_task_occurrences_wall_slot ON task_occurrences(task_id, definition_revision, wall_slot_key) WHERE wall_slot_key <> ''`,
	`CREATE INDEX IF NOT EXISTS idx_task_occurrences_task ON task_occurrences(task_id, nominal_at)`,

	`CREATE TABLE IF NOT EXISTS task_verifications (
 receipt_id TEXT PRIMARY KEY CHECK(receipt_id <> ''),
 run_id TEXT NOT NULL REFERENCES task_runs(run_id) ON DELETE RESTRICT,
 attempt_id TEXT REFERENCES task_attempts(attempt_id) ON DELETE RESTRICT,
 result_id TEXT REFERENCES turn_results(result_id) ON DELETE RESTRICT,
 checker_name TEXT NOT NULL CHECK(checker_name <> '' AND length(checker_name) <= 64),
 checker_version TEXT NOT NULL CHECK(checker_version <> '' AND length(checker_version) <= 64),
 checked_digest TEXT NOT NULL DEFAULT '' CHECK(length(checked_digest) IN (0,64)),
 check_outcome TEXT NOT NULL CHECK(check_outcome IN ('passed','failed','inconclusive')),
 checked_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_task_verifications_run ON task_verifications(run_id)`,

	// Terminal execution states are final; a reservation is released once and
	// only with exit/reconciliation evidence.
	`CREATE TRIGGER IF NOT EXISTS task_runs_terminal_final BEFORE UPDATE OF execution_state ON task_runs
 WHEN OLD.execution_state IN (` + terminalStatesSQL + `) AND NEW.execution_state<>OLD.execution_state
 BEGIN SELECT RAISE(ABORT, 'terminal run state is final'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_runs_identity_immutable BEFORE UPDATE OF
 run_id, task_id, definition_revision, nominal_at, namespace_id, namespace_epoch, registration_revision, chat_jid, chat_folder,
 thread_id, visibility, actor_ref, trigger_kind, session_kind, delivery_mode, created_at ON task_runs
 BEGIN SELECT RAISE(ABORT, 'run identity is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_attempts_terminal_final BEFORE UPDATE OF execution_state ON task_attempts
 WHEN OLD.execution_state IN (` + terminalStatesSQL + `) AND NEW.execution_state<>OLD.execution_state
 BEGIN SELECT RAISE(ABORT, 'terminal attempt state is final'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_attempts_identity_immutable BEFORE UPDATE OF attempt_id, run_id, attempt_no, turn_id, created_at ON task_attempts
 BEGIN SELECT RAISE(ABORT, 'attempt identity is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_attempts_reservation_release BEFORE UPDATE OF reservation_state ON task_attempts
 WHEN (NEW.reservation_state='released' AND NEW.exit_evidence='')
  OR (OLD.reservation_state='released' AND NEW.reservation_state<>'released')
  OR (OLD.reservation_state='held' AND NEW.reservation_state='none')
 BEGIN SELECT RAISE(ABORT, 'reservation release needs evidence and cannot be undone'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_attempts_snapshot_final BEFORE UPDATE OF provider, model, profile_fingerprint ON task_attempts
 WHEN OLD.provider<>'' AND (NEW.provider<>OLD.provider OR NEW.model<>OLD.model OR NEW.profile_fingerprint<>OLD.profile_fingerprint)
 BEGIN SELECT RAISE(ABORT, 'an attempt keeps its provider snapshot'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_occurrences_immutable BEFORE UPDATE ON task_occurrences
 BEGIN SELECT RAISE(ABORT, 'occurrence decisions are immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_verifications_immutable BEFORE UPDATE ON task_verifications
 BEGIN SELECT RAISE(ABORT, 'verification receipts are immutable'); END`,

	`CREATE TRIGGER IF NOT EXISTS task_runs_no_delete BEFORE DELETE ON task_runs
 BEGIN SELECT RAISE(ABORT, 'managed run evidence is never deleted automatically'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_attempts_no_delete BEFORE DELETE ON task_attempts
 BEGIN SELECT RAISE(ABORT, 'managed attempt evidence is never deleted automatically'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_occurrences_no_delete BEFORE DELETE ON task_occurrences
 BEGIN SELECT RAISE(ABORT, 'managed occurrence evidence is never deleted automatically'); END`,
	`CREATE TRIGGER IF NOT EXISTS task_verifications_no_delete BEFORE DELETE ON task_verifications
 BEGIN SELECT RAISE(ABORT, 'verification receipts are never deleted automatically'); END`,
}

// applyTaskSchema adds the task columns and ledger tables. Idempotent.
func (s *Store) applyTaskSchema(ctx context.Context) error {
	for _, stmt := range taskColumnMigrations {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil && !isDuplicateColumn(err) {
			return fmt.Errorf("task column migration: %w\n--- sql ---\n%s", err, stmt)
		}
	}
	for i, stmt := range taskSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("task statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	return nil
}
