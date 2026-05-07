//go:build integration

package ipc

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func TestIntegration_IPCRoundtrip(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	silentLog := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, filepath.Join(dir, "store.sqlite"), silentLog)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer st.Close()
	if err := st.RegisterChat(ctx, store.RegisteredChat{
		JID: "1001", Folder: "owner", IsOwner: true, AddedAt: 1700000000000,
	}); err != nil {
		t.Fatal(err)
	}

	root := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(root, "ipc", "owner", ".processed"), 0o755)
	os.MkdirAll(filepath.Join(root, "ipc", "owner", ".failed"), 0o755)
	writeIPC(t, root, "owner", "messages", "0001.json",
		map[string]string{"chatJid": "1001", "text": "integration ok"})

	msgs := &fakeMessageSink{}
	w := New(Config{
		DataDir: root, OwnerFolder: "owner", Tick: 100 * time.Millisecond,
	}, msgs, &fakeTaskSink{}, nil,
		func(folder string) (string, bool) {
			c, err := st.GetByFolder(ctx, folder)
			if err != nil {
				return "", false
			}
			return c.JID, true
		})

	go w.Run(ctx)

	// Poll until the file is moved into .processed/ or the deadline
	// expires.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if got := dirEntries(t, filepath.Join(root, "ipc", "owner", ".processed")); len(got) == 1 {
			return // pass
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("integration: file never landed in .processed/")
}
