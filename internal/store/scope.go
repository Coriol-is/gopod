package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"sync/atomic"
)

// Visibility is the audience of a delivery inside one namespace. Private
// results never fall back to the conversation (CP-019).
type Visibility string

const (
	VisibilityConversation Visibility = "conversation"
	VisibilityPrivate      Visibility = "private"
)

func (v Visibility) valid() bool {
	return v == VisibilityConversation || v == VisibilityPrivate
}

// ErrInvalidConversation is returned for a malformed thread or visibility.
var ErrInvalidConversation = errors.New("store: invalid conversation scope")

// ConversationScope extends the one host scope identity (ScopeRef, owned by
// namespaces.go) with the ordering key of a delivery: thread and visibility.
// ScopeRef itself is intentionally unchanged because CP-020 compares it by
// value. Thread 0 is a literal conversation, never a wildcard.
type ConversationScope struct {
	Scope      ScopeRef
	ThreadID   int
	Visibility Visibility
}

func (c ConversationScope) validShape() bool {
	return ValidScopeIdentity(c.Scope) && c.ThreadID >= 0 && c.Visibility.valid()
}

// ScopeAuthority is the shared snapshot/validate contract reused by CP-004,
// CP-020, CP-021 and CP-022. Store implements it over namespace_ownership and
// registered_chats; actor and profile authorization stay with the host.
type ScopeAuthority interface {
	SnapshotScope(ctx context.Context, folder string) (ScopeRef, error)
	ValidateScope(ctx context.Context, expected ScopeRef) error
	SnapshotConversation(ctx context.Context, jid string, thread int, visibility Visibility) (ConversationScope, error)
	ValidateConversation(ctx context.Context, expected ConversationScope) error
}

var _ ScopeAuthority = (*Store)(nil)

// SnapshotConversation snapshots the registration for a trusted chat JID.
// Like SnapshotScope it enlists in a Store transaction carried by ctx.
func (s *Store) SnapshotConversation(ctx context.Context, jid string, thread int, visibility Visibility) (ConversationScope, error) {
	out := ConversationScope{ThreadID: thread, Visibility: visibility}
	if thread < 0 || !visibility.valid() {
		return ConversationScope{}, ErrInvalidConversation
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		ref, e := snapshotScopeQ(ctx, q, "jid = ?", jid)
		out.Scope = ref
		return e
	})
	if err != nil {
		return ConversationScope{}, err
	}
	return out, nil
}

// ValidateConversation fails closed when the registration revision, epoch,
// retirement state, thread or visibility no longer match the snapshot.
func (s *Store) ValidateConversation(ctx context.Context, expected ConversationScope) error {
	if !expected.validShape() {
		return ErrStaleScope
	}
	_, err := s.withTx(ctx, func(q taskSQL) error { return validateScopeQ(ctx, q, expected.Scope) })
	return err
}

var savepointSeq atomic.Uint64

// withTx runs fn atomically. Without an enclosing Store transaction it opens
// one and commits. When ctx already carries a Store transaction (see taskDB) it
// enlists in it under a SAVEPOINT, so a failed fn leaves no partial writes and
// no nested transaction is started on the single connection. enlisted reports
// that the outer owner, not this call, decides whether the work is durable.
func (s *Store) withTx(ctx context.Context, fn func(q taskSQL) error) (enlisted bool, err error) {
	if outer, ok := s.taskDB(ctx).(*sql.Tx); ok {
		name := fmt.Sprintf("gopod_sp_%d", savepointSeq.Add(1))
		if _, err = outer.ExecContext(ctx, "SAVEPOINT "+name); err != nil {
			return true, err
		}
		if err = fn(outer); err != nil {
			if _, rbErr := outer.ExecContext(ctx, "ROLLBACK TO "+name); rbErr != nil {
				return true, errors.Join(err, rbErr)
			}
			_, _ = outer.ExecContext(ctx, "RELEASE "+name)
			return true, err
		}
		_, err = outer.ExecContext(ctx, "RELEASE "+name)
		return true, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return false, err
	}
	return false, tx.Commit()
}

// newUUID returns a random RFC 4122 version 4 identifier for host-owned rows.
func newUUID() (string, error) { return newNamespaceID() }

// canonicalHasher builds length-prefixed SHA-256 digests so that field
// boundaries are unambiguous (no concatenation collisions).
type canonicalHasher struct{ buf []byte }

func newCanonicalHasher(tag string) *canonicalHasher {
	h := &canonicalHasher{}
	return h.str(tag)
}

func (h *canonicalHasher) str(v string) *canonicalHasher { return h.bytes([]byte(v)) }

func (h *canonicalHasher) bytes(v []byte) *canonicalHasher {
	h.buf = binary.AppendUvarint(h.buf, uint64(len(v)))
	h.buf = append(h.buf, v...)
	return h
}

func (h *canonicalHasher) int(v int64) *canonicalHasher {
	h.buf = binary.AppendVarint(h.buf, v)
	return h
}

func (h *canonicalHasher) sum() string {
	d := sha256.Sum256(h.buf)
	return hex.EncodeToString(d[:])
}

func sha256Hex(b []byte) string {
	d := sha256.Sum256(b)
	return hex.EncodeToString(d[:])
}
