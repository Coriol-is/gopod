package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// TriggerMode says which messages of a registered group start an agent turn
// (D048). It is a per-registration setting; it never changes what Telegram
// delivers to the bot.
type TriggerMode string

const (
	// TriggerOwner answers only messages sent by the owner's own user id.
	TriggerOwner TriggerMode = "owner"
	// TriggerMention answers messages that address the bot.
	TriggerMention TriggerMode = "mention"
	// TriggerOwnerMention answers the owner always and others when addressed.
	TriggerOwnerMention TriggerMode = "owner+mention"
	// TriggerAll answers every message the bot receives.
	TriggerAll TriggerMode = "all"

	// LegacyTriggerMode applies to registrations that have no stored mode:
	// groups registered before D048 keep today's behavior.
	LegacyTriggerMode = TriggerAll
	// NewGroupTriggerMode is stored when a group is registered.
	NewGroupTriggerMode = TriggerMention
)

// ErrInvalidTriggerMode is returned for a mode outside the four values.
var ErrInvalidTriggerMode = errors.New("store: invalid trigger mode")

// ParseTriggerMode validates a user-typed mode.
func ParseTriggerMode(s string) (TriggerMode, error) {
	m := TriggerMode(strings.ToLower(strings.TrimSpace(s)))
	switch m {
	case TriggerOwner, TriggerMention, TriggerOwnerMention, TriggerAll:
		return m, nil
	}
	return "", ErrInvalidTriggerMode
}

const groupTriggerSchema = `CREATE TABLE IF NOT EXISTS group_triggers (
 jid TEXT PRIMARY KEY, namespace_id TEXT NOT NULL,
 mode TEXT NOT NULL CHECK(mode IN ('owner','mention','owner+mention','all')),
 updated_at INTEGER NOT NULL)`

func (s *Store) applyGroupTriggerSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, groupTriggerSchema); err != nil {
		return fmt.Errorf("group trigger schema: %w", err)
	}
	return nil
}

// GroupTrigger returns the trigger mode of a registered group. A registration
// without a stored mode (made before D048) answers LegacyTriggerMode. A row
// that belongs to an older registration of the same chat is ignored: the mode
// is scoped to the namespace, and survives its epoch rotations.
func (s *Store) GroupTrigger(ctx context.Context, jid string) (TriggerMode, error) {
	var mode string
	err := s.taskDB(ctx).QueryRowContext(ctx,
		`SELECT t.mode FROM group_triggers t JOIN registered_chats r
 ON r.jid = t.jid AND r.namespace_id = t.namespace_id WHERE t.jid = ?`, jid).Scan(&mode)
	if errors.Is(err, sql.ErrNoRows) {
		return LegacyTriggerMode, nil
	}
	if err != nil {
		return "", fmt.Errorf("store: GroupTrigger: %w", err)
	}
	return TriggerMode(mode), nil
}

// SetGroupTrigger stores the mode of a registered group.
func (s *Store) SetGroupTrigger(ctx context.Context, jid string, mode TriggerMode, nowMs int64) error {
	if _, err := ParseTriggerMode(string(mode)); err != nil {
		return err
	}
	rc, err := s.GetRegistered(ctx, jid)
	if err != nil {
		return err
	}
	return upsertGroupTrigger(ctx, s.taskDB(ctx), jid, rc.NamespaceID, mode, nowMs)
}

func upsertGroupTrigger(ctx context.Context, q taskSQL, jid, namespaceID string, mode TriggerMode, nowMs int64) error {
	_, err := q.ExecContext(ctx, `INSERT INTO group_triggers(jid,namespace_id,mode,updated_at) VALUES(?,?,?,?)
 ON CONFLICT(jid) DO UPDATE SET namespace_id=excluded.namespace_id, mode=excluded.mode, updated_at=excluded.updated_at`,
		jid, namespaceID, string(mode), nowMs)
	if err != nil {
		return fmt.Errorf("store: set group trigger: %w", err)
	}
	return nil
}

// seedNewGroupTrigger gives a freshly inserted group registration the default
// mode of new groups. Private chats and the owner chat are not groups.
func seedNewGroupTrigger(ctx context.Context, q taskSQL, jid, namespaceID string, addedAt int64) error {
	if !strings.HasPrefix(jid, "tg:-") {
		return nil
	}
	return upsertGroupTrigger(ctx, q, jid, namespaceID, NewGroupTriggerMode, addedAt)
}
