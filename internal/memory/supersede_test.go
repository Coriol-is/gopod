package memory

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func newSupersedeMemory(t *testing.T) (*Memory, *store.Store) {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), log)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return New(st, nil, log), st
}

// runWithDeadline runs trySupersede in a goroutine and fails if it does
// not return in time (the single-connection deadlock).
func runWithDeadline(t *testing.T, m *Memory, chat, keyword string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		m.trySupersede(ctx, chat, keyword)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(4 * time.Second):
		t.Fatal("trySupersede did not return: connection deadlock")
	}
	if ctx.Err() != nil {
		t.Fatalf("trySupersede ran into the context deadline: %v", ctx.Err())
	}
}

func TestTrySupersedeMarksMatchedMemory(t *testing.T) {
	m, st := newSupersedeMemory(t)
	_, err := st.DB().Exec(`INSERT INTO memories
		(chat_folder, kind, content, embed_model, created_at, updated_at)
		VALUES ('chat1', 'fact', 'owner lives in Lisbon', 'test', 1, 1)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	runWithDeadline(t, m, "chat1", "Lisbon")

	var status, typ string
	if err := st.DB().QueryRow(
		`SELECT status, typeof(updated_at) FROM memories WHERE chat_folder='chat1'`,
	).Scan(&status, &typ); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "superseded" {
		t.Errorf("status = %q, want superseded", status)
	}
	if typ != "integer" {
		t.Errorf("typeof(updated_at) = %q, want integer", typ)
	}
}

func TestTrySupersedeNoMatchReturns(t *testing.T) {
	m, st := newSupersedeMemory(t)
	_, err := st.DB().Exec(`INSERT INTO memories
		(chat_folder, kind, content, embed_model, created_at, updated_at)
		VALUES ('chat1', 'fact', 'owner lives in Lisbon', 'test', 1, 1)`)
	if err != nil {
		t.Fatalf("insert: %v", err)
	}

	runWithDeadline(t, m, "chat1", "Tokyo")

	var status string
	if err := st.DB().QueryRow(`SELECT status FROM memories`).Scan(&status); err != nil {
		t.Fatalf("select: %v", err)
	}
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
}
