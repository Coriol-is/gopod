package store

import (
	"context"
	"path/filepath"
	"testing"
)

func openSessionsStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestSessionsAreKeyedByThreadAndProvider(t *testing.T) {
	ctx := context.Background()
	s := openSessionsStore(t)
	want := map[[2]any]string{
		{0, "claude"}: "main-claude",
		{7, "claude"}: "thread7-claude",
		{0, "codex"}:  "main-codex",
	}
	for k, id := range want {
		if err := s.PutSession(ctx, "alice", k[0].(int), k[1].(string), id); err != nil {
			t.Fatalf("PutSession: %v", err)
		}
	}
	for k, id := range want {
		got, ok, err := s.GetSession(ctx, "alice", k[0].(int), k[1].(string))
		if err != nil || !ok || got != id {
			t.Errorf("GetSession(%v) = %q, %v, %v; want %q", k, got, ok, err, id)
		}
	}
	if _, ok, _ := s.GetSession(ctx, "alice", 8, "claude"); ok {
		t.Error("unexpected session for thread 8")
	}
	// Put again replaces.
	if err := s.PutSession(ctx, "alice", 7, "claude", "new"); err != nil {
		t.Fatal(err)
	}
	if got, _, _ := s.GetSession(ctx, "alice", 7, "claude"); got != "new" {
		t.Errorf("replace: got %q", got)
	}
}

func TestDeleteSessionOnlyRemovesOne(t *testing.T) {
	ctx := context.Background()
	s := openSessionsStore(t)
	_ = s.PutSession(ctx, "alice", 0, "claude", "a")
	_ = s.PutSession(ctx, "alice", 7, "claude", "b")
	_ = s.PutSession(ctx, "alice", 7, "codex", "c")
	if err := s.DeleteSession(ctx, "alice", 7, "claude"); err != nil {
		t.Fatalf("DeleteSession: %v", err)
	}
	if _, ok, _ := s.GetSession(ctx, "alice", 7, "claude"); ok {
		t.Error("deleted session still present")
	}
	for _, c := range []struct {
		th int
		p  string
	}{{0, "claude"}, {7, "codex"}} {
		if _, ok, _ := s.GetSession(ctx, "alice", c.th, c.p); !ok {
			t.Errorf("session (%d,%s) was removed", c.th, c.p)
		}
	}
	// Deleting a missing row is not an error.
	if err := s.DeleteSession(ctx, "alice", 99, "claude"); err != nil {
		t.Errorf("delete missing: %v", err)
	}
}

func TestMigrationKeepsExistingRowsAsMain(t *testing.T) {
	ctx := context.Background()
	s := openSessionsStore(t)
	db := s.DB()
	// Recreate the legacy shape and legacy turns (no thread_id).
	for _, q := range []string{
		`DROP TABLE sessions`,
		`CREATE TABLE sessions (chat_folder TEXT PRIMARY KEY, session_id TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
		`INSERT INTO sessions VALUES ('alice', 'old-id', 123)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	for i := 0; i < 2; i++ { // second run must be a no-op
		if err := s.applySchema(ctx); err != nil {
			t.Fatalf("applySchema run %d: %v", i, err)
		}
		got, ok, err := s.GetSession(ctx, "alice", 0, "claude")
		if err != nil || !ok || got != "old-id" {
			t.Fatalf("run %d: GetSession = %q, %v, %v", i, got, ok, err)
		}
	}
	var updated int64
	if err := db.QueryRowContext(ctx, `SELECT updated_at FROM sessions WHERE chat_folder='alice'`).Scan(&updated); err != nil || updated != 123 {
		t.Errorf("updated_at = %d, %v; want 123", updated, err)
	}
	if err := s.PutSession(ctx, "alice", 7, "claude", "t7"); err != nil {
		t.Fatalf("PutSession after migration: %v", err)
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('turns') WHERE name='thread_id'`).Scan(&n); err != nil || n != 1 {
		t.Errorf("turns.thread_id present = %d, %v", n, err)
	}
}
