package ipc

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestTickOnceWalksFoldersInSortedOrder(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{"charlie", "alice", "bob"} {
		if err := os.MkdirAll(filepath.Join(dir, "ipc", f), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
	}

	var (
		mu   sync.Mutex
		seen []string
	)
	w := New(Config{DataDir: dir}, nil, nil, nil, nil)
	w.handleFolderHook = func(_ context.Context, folder string) {
		mu.Lock()
		seen = append(seen, folder)
		mu.Unlock()
	}

	w.tickOnce(context.Background())

	want := []string{"alice", "bob", "charlie"}
	if len(seen) != len(want) {
		t.Fatalf("seen = %v, want %v", seen, want)
	}
	for i, f := range want {
		if seen[i] != f {
			t.Errorf("seen[%d] = %q, want %q", i, seen[i], f)
		}
	}
}

func TestTickOnceMissingIPCDirIsNoop(t *testing.T) {
	dir := t.TempDir() // no ipc/ subdir
	w := New(Config{DataDir: dir}, nil, nil, nil, nil)
	called := false
	w.handleFolderHook = func(context.Context, string) { called = true }
	w.tickOnce(context.Background())
	if called {
		t.Error("handleFolder was called for missing ipc dir")
	}
}
