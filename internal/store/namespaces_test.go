package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func TestNamespaceIdentityAndStaleRevision(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	c := RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 1}
	if err := st.RegisterChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	ref, err := st.SnapshotScope(ctx, c.Folder)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidScopeIdentity(ref) || ref.NamespaceEpoch != 1 || ref.RegistrationRevision != 1 {
		t.Fatalf("invalid scope: %+v", ref)
	}
	c.NamespaceID = "forged"
	c.NamespaceEpoch = 999
	c.RegistrationRevision = 999
	c.GroupEpochReady = true
	if err = st.RegisterChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	again, _ := st.SnapshotScope(ctx, c.Folder)
	if again != ref {
		t.Fatal("caller changed immutable scope")
	}
	stored, _ := st.GetRegistered(ctx, c.JID)
	if stored.GroupEpochReady {
		t.Fatal("caller qualified group")
	}
	c.Name = "new name"
	if err = st.RegisterChat(ctx, c); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(st.ValidateScope(ctx, ref), ErrStaleScope) {
		t.Fatal("old scope accepted")
	}
	c.Folder = "other-folder"
	if !errors.Is(st.RegisterChat(ctx, c), ErrNamespaceIdentityConflict) {
		t.Fatal("workspace reassigned")
	}
	c.Folder = "group-a"
	c.IsOwner = true
	if !errors.Is(st.RegisterChat(ctx, c), ErrNamespaceIdentityConflict) {
		t.Fatal("owner promotion")
	}
}

func TestNamespaceRetiredAliasCannotTransfer(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if err := st.RegisterChat(ctx, RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ref, _ := st.SnapshotScope(ctx, "group-a")
	// Simulate future unregister; the DB trigger reserves identity even when the
	// active registration is removed. No runtime or personal content is involved.
	if _, err := st.db.ExecContext(ctx, `DELETE FROM registered_chats WHERE jid='tg:-2'`); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(st.ValidateScope(ctx, ref), ErrStaleScope) {
		t.Fatal("removed scope accepted")
	}
	for _, jid := range []string{"tg:-3", "tg:-2"} {
		if !errors.Is(st.RegisterChat(ctx, RegisteredChat{JID: jid, Folder: "group-a", AddedAt: 2}), ErrNamespaceIdentityConflict) {
			t.Fatal("retired alias reused")
		}
	}
}

func TestScopeUsesExistingTransaction(t *testing.T) {
	st := newTestStore(t)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := st.RegisterChat(ctx, RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ref, _ := st.SnapshotScope(ctx, "group-a")
	if _, err := st.ApplyTaskRequest(ctx, "group-a", "request.json", "hash", 2, func(inner context.Context) ([]byte, error) {
		got, e := st.SnapshotScope(inner, "group-a")
		if e != nil {
			return nil, e
		}
		if got != ref {
			t.Fatal("scope changed")
		}
		return []byte("ok"), st.ValidateScope(inner, ref)
	}); err != nil {
		t.Fatal(err)
	}
}

func TestNamespaceBackfillAndOwnershipConflict(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if err := st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	ref, _ := st.SnapshotScope(ctx, "owner")
	if err := st.initializeNamespaceMetadata(ctx); err != nil {
		t.Fatal(err)
	}
	again, _ := st.SnapshotScope(ctx, "owner")
	if again != ref {
		t.Fatal("backfill reassigned identity")
	}
	if _, err := st.db.ExecContext(ctx, `UPDATE namespace_ownership SET chat_jid='tg:foreign' WHERE folder='owner'`); err != nil {
		t.Fatal(err)
	}
	if err := st.initializeNamespaceMetadata(ctx); err == nil {
		t.Fatal("conflicting ownership accepted")
	}
}

func TestLegacyRegistrationsGetStableNamespaceMetadata(t *testing.T) {
	path := filepath.Join(t.TempDir(), "legacy.sqlite")
	db, err := sql.Open("sqlite3", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE registered_chats (jid TEXT PRIMARY KEY,name TEXT,folder TEXT NOT NULL UNIQUE,trigger_pattern TEXT,requires_trigger INTEGER NOT NULL DEFAULT 1,is_owner INTEGER NOT NULL DEFAULT 0,added_at INTEGER NOT NULL); INSERT INTO registered_chats(jid,folder,is_owner,added_at) VALUES('tg:1','owner',1,1),('tg:-2','group-a',0,2)`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	st, err := Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner, err := st.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	group, err := st.SnapshotScope(t.Context(), "group-a")
	if err != nil {
		t.Fatal(err)
	}
	if owner.NamespaceID == group.NamespaceID || owner.Folder != "owner" || group.Folder != "group-a" {
		t.Fatal("legacy identities changed or collided")
	}
	st.Close()
	st, err = Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.ValidateScope(t.Context(), owner); err != nil {
		t.Fatal(err)
	}
	if err = st.ValidateScope(t.Context(), group); err != nil {
		t.Fatal(err)
	}
}
