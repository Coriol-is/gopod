package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
)

// folderPattern restricts chat folder names to a small alphabet that
// is safe to use as a directory name, a Docker label value, and a
// container name suffix all at once. Mirrors the chat folder regex
// validated by mountsec for AllowedChats entries.
var folderPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

// RegisteredChat is one row in the registered_chats table — a chat
// gopod has a workspace folder for and is willing to spawn an agent
// container for. Chats gopod has merely *seen* (chats table) are
// not the same thing as chats it is *registered* with.
type RegisteredChat struct {
	JID                  string
	Name                 string
	Folder               string
	TriggerPattern       string
	RequiresTrigger      bool
	IsOwner              bool
	AddedAt              int64 // unix ms
	NamespaceID          string
	NamespaceEpoch       int64
	RegistrationRevision int64
	GroupEpochReady      bool // clean epoch completed by host; not a runtime qualification grant
}

// ErrChatNotRegistered is returned by GetRegistered when no row exists.
var ErrChatNotRegistered = errors.New("store: chat not registered")

// RegisterChat inserts or updates a registered_chats row.
//
// Idempotent metadata updates preserve namespace, folder, owner flag and AddedAt.
// Workspace transfer is forbidden; explicit clean epoch migration is separate.
//
// folder must match folderPattern (the same regex mountsec uses for
// AllowedChats entries) so registered chat folders can never become
// path-traversal vectors or weird Docker label values.
func (s *Store) RegisterChat(ctx context.Context, c RegisteredChat) error {
	if c.JID == "" {
		return errors.New("store: RegisterChat: empty JID")
	}
	if !folderPattern.MatchString(c.Folder) {
		return fmt.Errorf("store: RegisterChat: folder %q does not match %s",
			c.Folder, folderPattern.String())
	}
	if c.AddedAt == 0 {
		return errors.New("store: RegisterChat: AddedAt is zero")
	}

	if c.Folder == "owner" && !c.IsOwner {
		return errors.New("store: reserved personal workspace")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var old RegisteredChat
	old, err = readRegistered(ctx, tx, "jid = ?", c.JID)
	if err == nil {
		if old.Folder != c.Folder || old.IsOwner != c.IsOwner {
			return ErrNamespaceIdentityConflict
		}
		if old.Name == c.Name && old.TriggerPattern == c.TriggerPattern && old.RequiresTrigger == c.RequiresTrigger {
			return tx.Commit()
		}
		_, err = tx.ExecContext(ctx, `UPDATE registered_chats SET name=?,trigger_pattern=?,requires_trigger=?,registration_revision=registration_revision+1 WHERE jid=?`, nullableString(c.Name), nullableString(c.TriggerPattern), boolToInt(c.RequiresTrigger), c.JID)
	} else if errors.Is(err, ErrChatNotRegistered) {
		var claimed int
		if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM namespace_ownership WHERE folder=?`, c.Folder).Scan(&claimed); err != nil {
			return err
		}
		if claimed != 0 {
			return ErrNamespaceIdentityConflict
		}
		id, e := newNamespaceID()
		if e != nil {
			return e
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO registered_chats(jid,name,folder,trigger_pattern,requires_trigger,is_owner,added_at,namespace_id,namespace_epoch,registration_revision,group_epoch_ready) VALUES(?,?,?,?,?,?,?,?,1,1,0)`, c.JID, nullableString(c.Name), c.Folder, nullableString(c.TriggerPattern), boolToInt(c.RequiresTrigger), boolToInt(c.IsOwner), c.AddedAt, id)
		if err == nil {
			_, err = tx.ExecContext(ctx, `INSERT INTO namespace_ownership(folder,namespace_id,chat_jid,namespace_epoch,created_at) VALUES(?,?,?,1,?)`, c.Folder, id, c.JID, c.AddedAt)
		}
	}
	if err != nil {
		return fmt.Errorf("store: RegisterChat: %w", err)
	}
	return tx.Commit()
}

// GetRegistered fetches one registered chat by jid. Returns
// ErrChatNotRegistered if no row exists.
func (s *Store) GetRegistered(ctx context.Context, jid string) (RegisteredChat, error) {
	return readRegistered(ctx, s.taskDB(ctx), "jid = ?", jid)
}

// IsRegistered is a thin convenience wrapper around GetRegistered for
// callers that only need a yes/no answer.
func (s *Store) IsRegistered(ctx context.Context, jid string) (bool, error) {
	_, err := s.GetRegistered(ctx, jid)
	if errors.Is(err, ErrChatNotRegistered) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// GetByFolder returns the registered chat with the given folder name.
// Returns ErrChatNotRegistered if no chat owns this folder.
func (s *Store) GetByFolder(ctx context.Context, folder string) (RegisteredChat, error) {
	return readRegistered(ctx, s.taskDB(ctx), "folder = ?", folder)
}

// ListRegistered returns every registered chat ordered by added_at
// (oldest first). Cheap on personal-assistant scale; if it ever needs
// pagination it's a self-contained refactor.
func (s *Store) ListRegistered(ctx context.Context) ([]RegisteredChat, error) {
	rows, err := s.taskDB(ctx).QueryContext(ctx, registeredColumns+" ORDER BY added_at ASC")
	if err != nil {
		return nil, fmt.Errorf("store: ListRegistered: %w", err)
	}
	defer rows.Close()
	var out []RegisteredChat
	for rows.Next() {
		c, e := scanRegistered(rows)
		if e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

const registeredColumns = `SELECT jid, IFNULL(name,''),folder,IFNULL(trigger_pattern,''),requires_trigger,is_owner,added_at,namespace_id,namespace_epoch,registration_revision,group_epoch_ready FROM registered_chats`

type registeredScanner interface{ Scan(...any) error }

func scanRegistered(row registeredScanner) (RegisteredChat, error) {
	var c RegisteredChat
	var trigger, owner, ready int
	err := row.Scan(&c.JID, &c.Name, &c.Folder, &c.TriggerPattern, &trigger, &owner, &c.AddedAt, &c.NamespaceID, &c.NamespaceEpoch, &c.RegistrationRevision, &ready)
	c.RequiresTrigger, c.IsOwner, c.GroupEpochReady = trigger != 0, owner != 0, ready != 0
	return c, err
}
func readRegistered(ctx context.Context, q taskSQL, predicate string, value any) (RegisteredChat, error) {
	c, err := scanRegistered(q.QueryRowContext(ctx, registeredColumns+" WHERE "+predicate, value))
	if errors.Is(err, sql.ErrNoRows) {
		return RegisteredChat{}, ErrChatNotRegistered
	}
	return c, err
}
