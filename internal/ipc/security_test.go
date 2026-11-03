package ipc

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestArchiveRejectsSymlinkToSource(t *testing.T) {
	for _, channel := range []string{"messages", "tasks"} {
		for _, archive := range []string{".processed", ".failed"} {
			t.Run(channel+archive, func(t *testing.T) {
				data := t.TempDir()
				writeIPC(t, data, "alice", channel, "a.json", map[string]string{"text": "hello"})
				base := filepath.Join(data, "ipc", "alice")
				if err := os.Remove(filepath.Join(base, archive)); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(channel, filepath.Join(base, archive)); err != nil {
					t.Fatal(err)
				}
				w := New(Config{DataDir: data}, nil, nil, nil, nil)
				source := filepath.Join(base, channel, "a.json")
				if err := w.archiveFile("alice", source, archive); err == nil {
					t.Fatal("archive accepted symlink")
				}
				if _, err := os.Stat(source); err != nil {
					t.Fatal("request lost", err)
				}
				calls := 0
				for range 2 { // restart cannot turn the symlink into repeated side effects
					w = New(Config{DataDir: data}, nil, nil, nil, nil)
					w.processChannel(t.Context(), "alice", channel, func(context.Context, string, string) bool { calls++; return true })
				}
				if calls != 0 {
					t.Fatal("unsafe archive allowed dispatch")
				}
			})
		}
	}
}

func TestReceiptsRejectIntermediateAndFinalSymlinks(t *testing.T) {
	for _, component := range []string{"results", "results/messages"} {
		t.Run(component, func(t *testing.T) {
			data := t.TempDir()
			base := filepath.Join(data, "ipc", "alice")
			if err := os.MkdirAll(filepath.Join(base, "tasks"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(filepath.Join(base, component)), 0700); err != nil {
				t.Fatal(err)
			}
			target := "tasks"
			if component == "results/messages" {
				target = "../tasks"
			}
			if err := os.Symlink(target, filepath.Join(base, component)); err != nil {
				t.Fatal(err)
			}
			w := New(Config{DataDir: data}, nil, nil, nil, nil)
			if w.writeReceiptNamed("alice", "a.json", "results/messages", map[string]string{"op": "schedule"}) {
				t.Fatal("receipt followed symlink")
			}
			if entries := dirEntries(t, filepath.Join(base, "tasks")); len(entries) != 0 {
				t.Fatal("receipt injected request", entries)
			}
		})
	}
}

func TestChannelDispatchBudgetAndProgress(t *testing.T) {
	data := t.TempDir()
	for i := 0; i < maxChannelDispatches+5; i++ {
		writeIPC(t, data, "alice", "messages", fmt.Sprintf("%04d.json", i), map[string]string{"text": "hello"})
	}
	w := New(Config{DataDir: data}, nil, nil, nil, nil)
	calls := 0
	handler := func(_ context.Context, folder, path string) bool { calls++; return w.moveProcessed(folder, path) }
	w.processChannel(t.Context(), "alice", "messages", handler)
	if calls != maxChannelDispatches {
		t.Fatalf("first tick calls=%d", calls)
	}
	w.processChannel(t.Context(), "alice", "messages", handler)
	if calls != maxChannelDispatches+5 {
		t.Fatalf("backlog did not progress: %d", calls)
	}
}

func TestWatcherStateBoundedAndMissingRequestsPruned(t *testing.T) {
	w := New(Config{DataDir: t.TempDir()}, nil, nil, nil, nil)
	for i := 0; i < maxWatcherEntries+5; i++ {
		key := fmt.Sprintf("missing-%d", i)
		w.bumpRetry(key)
		w.writtenBound(key, time.Time{})
		w.rememberReceipt("owner", key, "queued")
	}
	if len(w.retries) > maxWatcherEntries || len(w.writeFloor) > maxWatcherEntries || len(w.receiptStates) > maxWatcherEntries {
		t.Fatal("unbounded watcher map")
	}
	w.pruneWatcherState()
	if len(w.retries) != 0 || len(w.writeFloor) != 0 {
		t.Fatal("removed requests leaked state")
	}
}

func TestPruneDoesNotFollowResultsSymlink(t *testing.T) {
	data := t.TempDir()
	base := filepath.Join(data, "ipc", "alice")
	for i := 0; i < 3; i++ {
		writeIPC(t, data, "alice", "messages", fmt.Sprintf("%d.json", i), map[string]string{"text": "keep"})
	}
	if err := os.Symlink("messages", filepath.Join(base, "results")); err != nil {
		t.Fatal(err)
	}
	w := New(Config{DataDir: data, Retention: 1}, nil, nil, nil, nil)
	w.pruneNamespace("alice")
	if len(dirEntries(t, filepath.Join(base, "messages"))) != 3 {
		t.Fatal("pruning followed results symlink into requests")
	}
}
