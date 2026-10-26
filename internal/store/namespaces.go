package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

var (
	ErrNamespaceIdentityConflict = errors.New("store: namespace identity or workspace ownership conflict")
	ErrStaleScope                = errors.New("store: namespace scope is stale or unavailable")
	namespaceIDPattern           = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
)

// ScopeRef is a host-owned admission snapshot. Folder is the current storage key,
// never a transferable authorization alias. No actor authority is inferred here.
// Background publications and future CP-002 outbox records reuse this identity.
type ScopeRef struct {
	NamespaceID          string
	NamespaceEpoch       int64
	RegistrationRevision int64
	JID                  string
	Folder               string
}

func (c RegisteredChat) Scope() ScopeRef {
	return ScopeRef{NamespaceID: c.NamespaceID, NamespaceEpoch: c.NamespaceEpoch, RegistrationRevision: c.RegistrationRevision, JID: c.JID, Folder: c.Folder}
}

func newNamespaceID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("namespace identity: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// ValidScopeIdentity checks opaque identifiers before they become filesystem keys.
func ValidScopeIdentity(ref ScopeRef) bool {
	return namespaceIDPattern.MatchString(ref.NamespaceID) && ref.NamespaceEpoch > 0 && ref.RegistrationRevision > 0 && ref.JID != "" && folderPattern.MatchString(ref.Folder)
}

func (s *Store) initializeNamespaceMetadata(ctx context.Context) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, registeredColumns)
	if err != nil {
		return err
	}
	var chats []RegisteredChat
	for rows.Next() {
		c, e := scanRegistered(rows)
		if e != nil {
			rows.Close()
			return e
		}
		chats = append(chats, c)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, c := range chats {
		if c.NamespaceID == "" {
			c.NamespaceID, err = newNamespaceID()
			if err != nil {
				return err
			}
			if _, err = tx.ExecContext(ctx, `UPDATE registered_chats SET namespace_id=? WHERE jid=? AND namespace_id=''`, c.NamespaceID, c.JID); err != nil {
				return err
			}
		}
		if !ValidScopeIdentity(c.Scope()) {
			return ErrNamespaceIdentityConflict
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO namespace_ownership(folder,namespace_id,chat_jid,namespace_epoch,created_at) VALUES(?,?,?,?,?) ON CONFLICT(folder) DO NOTHING`, c.Folder, c.NamespaceID, c.JID, c.NamespaceEpoch, c.AddedAt); err != nil {
			return err
		}
		if err = validateOwnership(ctx, tx, c.Scope()); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `CREATE UNIQUE INDEX IF NOT EXISTS idx_registered_namespace_identity ON registered_chats(namespace_id)`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `CREATE TRIGGER IF NOT EXISTS namespace_registration_delete AFTER DELETE ON registered_chats BEGIN UPDATE namespace_ownership SET retired=1,retired_at=unixepoch('subsec')*1000 WHERE folder=old.folder; END`); err != nil {
		return err
	}
	return tx.Commit()
}

func validateOwnership(ctx context.Context, q taskSQL, ref ScopeRef) error {
	if !ValidScopeIdentity(ref) {
		return ErrStaleScope
	}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM namespace_ownership WHERE folder=? AND namespace_id=? AND chat_jid=? AND namespace_epoch=? AND retired=0`, ref.Folder, ref.NamespaceID, ref.JID, ref.NamespaceEpoch).Scan(&n); err != nil {
		return err
	}
	if n != 1 {
		return ErrStaleScope
	}
	return nil
}

// SnapshotScope and ValidateScope enlist an existing Store task transaction from
// ctx. They never start a nested transaction or use another SQLite connection.
func (s *Store) SnapshotScope(ctx context.Context, folder string) (ScopeRef, error) {
	if tx, ok := s.taskDB(ctx).(*sql.Tx); ok {
		return s.SnapshotScopeTx(ctx, tx, folder)
	}
	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ScopeRef{}, err
	}
	defer tx.Rollback()
	ref, err := s.SnapshotScopeTx(ctx, tx, folder)
	if err != nil {
		return ScopeRef{}, err
	}
	if err = tx.Commit(); err != nil {
		return ScopeRef{}, err
	}
	return ref, nil
}

func (s *Store) ValidateScope(ctx context.Context, expected ScopeRef) error {
	got, err := s.SnapshotScope(ctx, expected.Folder)
	if err != nil {
		return err
	}
	if got != expected {
		return ErrStaleScope
	}
	return nil
}

// SnapshotScopeTx and ValidateScopeTx are explicit variants for Store-owned SQL
// transactions that do not use the context enlistment helper.
func (s *Store) SnapshotScopeTx(ctx context.Context, tx *sql.Tx, folder string) (ScopeRef, error) {
	if tx == nil {
		return ScopeRef{}, ErrStaleScope
	}
	c, err := readRegistered(ctx, tx, "folder = ?", folder)
	if errors.Is(err, ErrChatNotRegistered) {
		return ScopeRef{}, ErrStaleScope
	}
	if err != nil {
		return ScopeRef{}, err
	}
	ref := c.Scope()
	if err = validateOwnership(ctx, tx, ref); err != nil {
		return ScopeRef{}, err
	}
	return ref, nil
}
func (s *Store) ValidateScopeTx(ctx context.Context, tx *sql.Tx, expected ScopeRef) error {
	got, err := s.SnapshotScopeTx(ctx, tx, expected.Folder)
	if err != nil {
		return err
	}
	if got != expected {
		return ErrStaleScope
	}
	return nil
}

// ListNamespaceFolders is host-only ownership inventory for lifecycle guards.
// It includes retired keys so a legacy process cannot disappear from migration
// checks when the active registration moves to a clean epoch. It exposes no data.
func (s *Store) ListNamespaceFolders(ctx context.Context, namespaceID string) ([]string, error) {
	if !namespaceIDPattern.MatchString(namespaceID) {
		return nil, ErrNamespaceIdentityConflict
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT folder FROM namespace_ownership WHERE namespace_id=? ORDER BY namespace_epoch,folder`, namespaceID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var folder string
		if err = rows.Scan(&folder); err != nil {
			return nil, err
		}
		if !folderPattern.MatchString(folder) {
			return nil, ErrNamespaceIdentityConflict
		}
		out = append(out, folder)
	}
	return out, rows.Err()
}
