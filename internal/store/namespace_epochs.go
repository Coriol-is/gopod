package store

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// GroupEpochPreview contains metadata only. It never reads private chat content,
// changes schedules, copies auth, creates files or qualifies a runtime.
type GroupEpochPreview struct {
	Current     ScopeRef
	NextEpoch   int64
	ActiveTasks int
	Ready       bool
}

type GroupEpochMigration struct {
	PreviousScope ScopeRef
	CurrentScope  ScopeRef
	PausedTasks   int64
}

func groupJID(jid string) bool {
	if !strings.HasPrefix(jid, "tg:") {
		return false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(jid, "tg:"), 10, 64)
	return err == nil && id < 0 && jid == "tg:"+strconv.FormatInt(id, 10)
}

func (s *Store) PreviewGroupEpoch(ctx context.Context, jid string) (GroupEpochPreview, error) {
	c, err := s.GetRegistered(ctx, jid)
	if err != nil {
		return GroupEpochPreview{}, err
	}
	if !groupJID(c.JID) || c.IsOwner {
		return GroupEpochPreview{}, ErrNamespaceIdentityConflict
	}
	ref, err := s.SnapshotScope(ctx, c.Folder)
	if err != nil {
		return GroupEpochPreview{}, err
	}
	var count int
	if err = s.taskDB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM scheduled_tasks WHERE chat_folder=? AND status='active'`, c.Folder).Scan(&count); err != nil {
		return GroupEpochPreview{}, err
	}
	return GroupEpochPreview{Current: ref, NextEpoch: ref.NamespaceEpoch + 1, ActiveTasks: count, Ready: c.GroupEpochReady}, nil
}

// RotateGroupEpoch starts an explicit clean-epoch migration. The controller must
// hold the chat slot and confirm owned processes have settled before calling.
// All old DB rows and files stay under their retired storage key. Existing tasks
// are paused, never moved/replayed; personal rows and files are never touched.
// An interrupted migration stays unready until CompleteGroupEpoch is called after
// protected filesystem and effective runtime qualification. No billing/auth grant
// or migration approval can originate from an agent-written workspace file.
func (s *Store) RotateGroupEpoch(ctx context.Context, jid string, expectedRevision int64) (GroupEpochMigration, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return GroupEpochMigration{}, err
	}
	defer tx.Rollback()
	c, err := readRegistered(ctx, tx, "jid = ?", jid)
	if err != nil {
		return GroupEpochMigration{}, err
	}
	if !groupJID(c.JID) || c.IsOwner {
		return GroupEpochMigration{}, ErrNamespaceIdentityConflict
	}
	if c.RegistrationRevision != expectedRevision {
		return GroupEpochMigration{}, ErrStaleScope
	}
	previous := c.Scope()
	if err = validateOwnership(ctx, tx, previous); err != nil {
		return GroupEpochMigration{}, err
	}
	if c.NamespaceEpoch > 1 && !c.GroupEpochReady {
		return GroupEpochMigration{}, fmt.Errorf("store: clean epoch is already awaiting qualification")
	}
	epoch := c.NamespaceEpoch + 1
	if epoch <= c.NamespaceEpoch {
		return GroupEpochMigration{}, ErrNamespaceIdentityConflict
	}
	folder := "ns-" + strings.ReplaceAll(c.NamespaceID, "-", "") + "-e" + strconv.FormatInt(epoch, 10)
	now := time.Now().UnixMilli()
	if _, err = tx.ExecContext(ctx, `UPDATE namespace_ownership SET retired=1,retired_at=? WHERE folder=? AND namespace_id=? AND retired=0`, now, c.Folder, c.NamespaceID); err != nil {
		return GroupEpochMigration{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO namespace_ownership(folder,namespace_id,chat_jid,namespace_epoch,created_at) VALUES(?,?,?,?,?)`, folder, c.NamespaceID, c.JID, epoch, now); err != nil {
		return GroupEpochMigration{}, err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE registered_chats SET folder=?,namespace_epoch=?,registration_revision=registration_revision+1,group_epoch_ready=0 WHERE jid=? AND registration_revision=?`, folder, epoch, c.JID, expectedRevision); err != nil {
		return GroupEpochMigration{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE scheduled_tasks SET status='paused' WHERE chat_folder=? AND status='active'`, c.Folder)
	if err != nil {
		return GroupEpochMigration{}, err
	}
	paused, err := result.RowsAffected()
	if err != nil {
		return GroupEpochMigration{}, err
	}
	current := ScopeRef{NamespaceID: c.NamespaceID, NamespaceEpoch: epoch, RegistrationRevision: c.RegistrationRevision + 1, JID: c.JID, Folder: folder}
	if err = tx.Commit(); err != nil {
		return GroupEpochMigration{}, err
	}
	return GroupEpochMigration{PreviousScope: previous, CurrentScope: current, PausedTasks: paused}, nil
}

// CompleteGroupEpoch is the controller's final CAS after filesystem preparation
// and qualification. It marks only clean-epoch readiness; provider/network/tool
// policy and explicit host approval are separately enforced on every admission.
func (s *Store) CompleteGroupEpoch(ctx context.Context, expected ScopeRef) (ScopeRef, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ScopeRef{}, err
	}
	defer tx.Rollback()
	if err = s.ValidateScopeTx(ctx, tx, expected); err != nil {
		return ScopeRef{}, err
	}
	c, err := readRegistered(ctx, tx, "jid = ?", expected.JID)
	if err != nil {
		return ScopeRef{}, err
	}
	if !groupJID(c.JID) || c.IsOwner || c.NamespaceEpoch <= 1 {
		return ScopeRef{}, ErrNamespaceIdentityConflict
	}
	if c.GroupEpochReady {
		return c.Scope(), tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `UPDATE registered_chats SET group_epoch_ready=1,registration_revision=registration_revision+1 WHERE jid=? AND registration_revision=?`, expected.JID, expected.RegistrationRevision); err != nil {
		return ScopeRef{}, err
	}
	current := expected
	current.RegistrationRevision++
	return current, tx.Commit()
}

// SetNamespaceDisabled atomically binds an admission flag to the current group
// scope. Revocation need not wait behind a running model: it blocks future tool
// and runtime admission immediately without killing the active process.
func (s *Store) SetNamespaceDisabled(ctx context.Context, expected ScopeRef, disabled bool) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = s.ValidateScopeTx(ctx, tx, expected); err != nil {
		return err
	}
	c, err := readRegistered(ctx, tx, "jid = ?", expected.JID)
	if err != nil {
		return err
	}
	if !groupJID(c.JID) || c.IsOwner || !disabled && !c.GroupEpochReady {
		return ErrNamespaceIdentityConflict
	}
	value := "0"
	if disabled {
		value = "1"
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, "group_disabled:"+expected.NamespaceID, value); err != nil {
		return err
	}
	return tx.Commit()
}
