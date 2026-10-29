package store

import (
	"context"
	"fmt"
)

// pinnedByUnresolvedSQL is true for an artifact (bound to the placeholder in the
// enclosing query as a.artifact_id) that is referenced by a part which is still
// pending, sending or uncertain, or by a delivery group that is unresolved.
// Garbage collection and the deletion triggers use the same predicate, so no
// caller can bypass the pin.
const pinnedByUnresolvedSQL = `EXISTS (SELECT 1 FROM artifact_refs pr
 JOIN outbox_parts pp ON pp.part_id = pr.ref_id
 JOIN delivery_groups pg ON pg.delivery_id = pp.delivery_id
 WHERE pr.artifact_id = a.artifact_id AND pr.ref_kind = 'outbox_part'
 AND (pg.unresolved = 1 OR pp.state IN ('pending','sending','uncertain')))`

// artifactSchemaStatements are the CP-003 immutable artifact inventory tables
// (docs/superpowers/specs/2026-10-26-immutable-artifact-delivery.md). All are
// additive and idempotent. A published artifact is owned by references, never by
// a mutable agent path; only the host mutates these tables.
var artifactSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS artifact_reservations (
 preparation_id TEXT PRIMARY KEY,
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL,
 owner_ref TEXT NOT NULL DEFAULT '',
 bytes INTEGER NOT NULL CHECK(bytes > 0),
 file_count INTEGER NOT NULL CHECK(file_count > 0),
 state TEXT NOT NULL CHECK(state IN ('active','released')),
 created_at INTEGER NOT NULL, expires_at INTEGER NOT NULL)`,
	`CREATE INDEX IF NOT EXISTS idx_artifact_reservations_active ON artifact_reservations(namespace_id) WHERE state = 'active'`,

	`CREATE TABLE IF NOT EXISTS artifacts (
 artifact_id TEXT PRIMARY KEY,
 namespace_id TEXT NOT NULL, namespace_epoch INTEGER NOT NULL,
 state TEXT NOT NULL CHECK(state IN ('preparing','ready','quarantined','deleting')),
 relative_blob_key TEXT NOT NULL UNIQUE,
 sha256 TEXT NOT NULL DEFAULT '',
 size INTEGER NOT NULL DEFAULT 0 CHECK(size >= 0),
 media_type TEXT NOT NULL DEFAULT '',
 sanitized_filename TEXT NOT NULL DEFAULT '',
 reason_code TEXT NOT NULL DEFAULT '',
 created_at INTEGER NOT NULL,
 last_reference_at INTEGER NOT NULL DEFAULT 0,
 preparation_id TEXT NOT NULL REFERENCES artifact_reservations(preparation_id))`,
	`CREATE INDEX IF NOT EXISTS idx_artifacts_state ON artifacts(state)`,
	`CREATE INDEX IF NOT EXISTS idx_artifacts_preparation ON artifacts(preparation_id)`,
	`CREATE INDEX IF NOT EXISTS idx_artifacts_namespace ON artifacts(namespace_id)`,

	`CREATE TABLE IF NOT EXISTS artifact_refs (
 artifact_id TEXT NOT NULL REFERENCES artifacts(artifact_id),
 ref_kind TEXT NOT NULL CHECK(ref_kind IN ('outbox_part')),
 ref_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 UNIQUE(artifact_id, ref_kind, ref_id))`,
	`CREATE INDEX IF NOT EXISTS idx_artifact_refs_ref ON artifact_refs(ref_kind, ref_id)`,

	// Identity and content facts never change; the content hash and size are
	// fixed once an artifact leaves preparing.
	`CREATE TRIGGER IF NOT EXISTS artifacts_identity_immutable BEFORE UPDATE OF
 artifact_id, namespace_id, namespace_epoch, relative_blob_key, preparation_id, created_at ON artifacts
 BEGIN SELECT RAISE(ABORT, 'artifact identity is immutable'); END`,
	`CREATE TRIGGER IF NOT EXISTS artifacts_content_immutable BEFORE UPDATE OF sha256, size, media_type, sanitized_filename ON artifacts
 WHEN OLD.state <> 'preparing'
 BEGIN SELECT RAISE(ABORT, 'artifact content facts are immutable once ready'); END`,
	`CREATE TRIGGER IF NOT EXISTS artifact_refs_immutable BEFORE UPDATE ON artifact_refs
 BEGIN SELECT RAISE(ABORT, 'artifact references are immutable'); END`,

	// Defense in depth for "GC never deletes a pending or uncertain reference":
	// even a buggy caller cannot drop a pinned artifact, its references, or a
	// row that has not first been marked deleting.
	`DROP TRIGGER IF EXISTS artifacts_delete_guard`,
	`CREATE TRIGGER artifacts_delete_guard BEFORE DELETE ON artifacts
 WHEN OLD.state <> 'deleting'
 OR EXISTS (SELECT 1 FROM artifact_refs pr
  JOIN outbox_parts pp ON pp.part_id = pr.ref_id
  JOIN delivery_groups pg ON pg.delivery_id = pp.delivery_id
  WHERE pr.artifact_id = OLD.artifact_id AND pr.ref_kind = 'outbox_part'
  AND (pg.unresolved = 1 OR pp.state IN ('pending','sending','uncertain')))
 BEGIN SELECT RAISE(ABORT, 'artifact is pinned or not marked deleting'); END`,
	`DROP TRIGGER IF EXISTS artifact_refs_delete_guard`,
	`CREATE TRIGGER artifact_refs_delete_guard BEFORE DELETE ON artifact_refs
 WHEN EXISTS (SELECT 1 FROM outbox_parts pp JOIN delivery_groups pg ON pg.delivery_id = pp.delivery_id
  WHERE pp.part_id = OLD.ref_id AND OLD.ref_kind = 'outbox_part'
  AND (pg.unresolved = 1 OR pp.state IN ('pending','sending','uncertain')))
 BEGIN SELECT RAISE(ABORT, 'artifact reference is pinned by an unresolved part'); END`,
}

func (s *Store) applyArtifactSchema(ctx context.Context) error {
	for i, stmt := range artifactSchemaStatements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("artifact statement %d: %w\n--- sql ---\n%s", i, err, stmt)
		}
	}
	return nil
}
