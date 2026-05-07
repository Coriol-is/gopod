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
	JID             string
	Name            string
	Folder          string
	TriggerPattern  string
	RequiresTrigger bool
	IsOwner         bool
	AddedAt         int64 // unix ms
}

// ErrChatNotRegistered is returned by GetRegistered when no row exists.
var ErrChatNotRegistered = errors.New("store: chat not registered")

// RegisterChat inserts or updates a registered_chats row.
//
// Idempotent on jid: re-registering an existing chat updates the
// folder, name, trigger fields, and is_owner flag, but preserves the
// original added_at so the registration history stays meaningful.
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

	const stmt = `
		INSERT INTO registered_chats (
		  jid, name, folder, trigger_pattern, requires_trigger, is_owner, added_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(jid) DO UPDATE SET
		  name             = excluded.name,
		  folder           = excluded.folder,
		  trigger_pattern  = excluded.trigger_pattern,
		  requires_trigger = excluded.requires_trigger,
		  is_owner         = excluded.is_owner`

	if _, err := s.db.ExecContext(ctx, stmt,
		c.JID,
		nullableString(c.Name),
		c.Folder,
		nullableString(c.TriggerPattern),
		boolToInt(c.RequiresTrigger),
		boolToInt(c.IsOwner),
		c.AddedAt,
	); err != nil {
		return fmt.Errorf("store: RegisterChat: %w", err)
	}
	return nil
}

// GetRegistered fetches one registered chat by jid. Returns
// ErrChatNotRegistered if no row exists.
func (s *Store) GetRegistered(ctx context.Context, jid string) (RegisteredChat, error) {
	const q = `
		SELECT jid, IFNULL(name, ''), folder, IFNULL(trigger_pattern, ''),
		       requires_trigger, is_owner, added_at
		  FROM registered_chats
		 WHERE jid = ?`

	var c RegisteredChat
	var requiresTrigger, isOwner int
	err := s.db.QueryRowContext(ctx, q, jid).Scan(
		&c.JID, &c.Name, &c.Folder, &c.TriggerPattern,
		&requiresTrigger, &isOwner, &c.AddedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RegisteredChat{}, ErrChatNotRegistered
	}
	if err != nil {
		return RegisteredChat{}, fmt.Errorf("store: GetRegistered: %w", err)
	}
	c.RequiresTrigger = requiresTrigger != 0
	c.IsOwner = isOwner != 0
	return c, nil
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
	const q = `
		SELECT jid, IFNULL(name, ''), folder, IFNULL(trigger_pattern, ''),
		       requires_trigger, is_owner, added_at
		  FROM registered_chats
		 WHERE folder = ?`

	var c RegisteredChat
	var requiresTrigger, isOwner int
	err := s.db.QueryRowContext(ctx, q, folder).Scan(
		&c.JID, &c.Name, &c.Folder, &c.TriggerPattern,
		&requiresTrigger, &isOwner, &c.AddedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return RegisteredChat{}, ErrChatNotRegistered
	}
	if err != nil {
		return RegisteredChat{}, fmt.Errorf("store: GetByFolder: %w", err)
	}
	c.RequiresTrigger = requiresTrigger != 0
	c.IsOwner = isOwner != 0
	return c, nil
}

// ListRegistered returns every registered chat ordered by added_at
// (oldest first). Cheap on personal-assistant scale; if it ever needs
// pagination it's a self-contained refactor.
func (s *Store) ListRegistered(ctx context.Context) ([]RegisteredChat, error) {
	const q = `
		SELECT jid, IFNULL(name, ''), folder, IFNULL(trigger_pattern, ''),
		       requires_trigger, is_owner, added_at
		  FROM registered_chats
		 ORDER BY added_at ASC`

	rows, err := s.db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("store: ListRegistered: %w", err)
	}
	defer rows.Close()

	var out []RegisteredChat
	for rows.Next() {
		var c RegisteredChat
		var requiresTrigger, isOwner int
		if err := rows.Scan(
			&c.JID, &c.Name, &c.Folder, &c.TriggerPattern,
			&requiresTrigger, &isOwner, &c.AddedAt,
		); err != nil {
			return nil, fmt.Errorf("store: ListRegistered: scan: %w", err)
		}
		c.RequiresTrigger = requiresTrigger != 0
		c.IsOwner = isOwner != 0
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: ListRegistered: rows: %w", err)
	}
	return out, nil
}
