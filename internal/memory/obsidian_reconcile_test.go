package memory

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"
)

// Reconcile must archive a deleted file's chunks without holding the
// store's only connection: before the fix the UPDATE waited forever for
// it while the SELECT's rows were still open.
func TestReconcileArchivesDeletedFileWithoutDeadlock(t *testing.T) {
	m := newThreadTestMemory(t)
	bg := context.Background()
	id, err := m.Add(bg, "chat", "document", "gone", "chunk of a deleted note", "obsidian:gone.md#Intro")
	if err != nil {
		t.Fatal(err)
	}
	o := NewObsidianIngester(m, t.TempDir(), "chat", slog.New(slog.NewTextHandler(io.Discard, nil)))

	ctx, cancel := context.WithTimeout(bg, 5*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		o.Reconcile(ctx)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("Reconcile hung")
	}
	var status string
	if err := m.store.DB().QueryRowContext(bg,
		`SELECT IFNULL(status,'active') FROM memories WHERE id = ?`, id).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "archived" {
		t.Errorf("status = %s, want archived", status)
	}
}
