package ipc

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// Contents were already rooted, but unrooted ReadDir exposed outside basenames
// through error receipts and logs before the content read rejected the escape.
func TestWatcher_ChannelSymlinkDoesNotDiscloseOutsideNames(t *testing.T) {
	for _, channel := range []string{"messages", "tasks"} {
		t.Run(channel, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			privateName := "PRIVATE-OUTSIDE-FILENAME-CANARY.json"
			outsideFile := filepath.Join(outside, privateName)
			if err := os.WriteFile(outsideFile, []byte(`{"op":"list"}`), 0600); err != nil {
				t.Fatal(err)
			}
			namespace := filepath.Join(root, "ipc", "alice")
			if err := os.MkdirAll(namespace, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, filepath.Join(namespace, channel)); err != nil {
				t.Fatal(err)
			}
			var logs bytes.Buffer
			msgs := &fakeMessageSink{}
			w := New(Config{DataDir: root, OwnerFolder: "owner"}, msgs, &fakeTaskSink{}, slog.New(slog.NewTextHandler(&logs, nil)), staticResolver(map[string]string{"alice": "2002", "bob": "2003"}))
			// Positive control: a hostile channel cannot prevent another namespace's
			// valid message from being dispatched, receipted and archived.
			writeIPC(t, root, "bob", "messages", "healthy.json", map[string]string{"chatJid": "2003", "text": "healthy"})
			w.tickOnce(context.Background())
			if len(msgs.callsSnapshot()) != 1 {
				t.Fatal("healthy namespace did not dispatch exactly once")
			}
			if got := dirEntries(t, filepath.Join(root, "ipc", "bob", ".processed")); len(got) != 1 {
				t.Fatalf("healthy archive: %v", got)
			}
			if _, err := os.Stat(filepath.Join(root, "ipc", "bob", "results", "messages", "healthy.result.json")); err != nil {
				t.Fatal("healthy receipt missing", err)
			}
			if strings.Contains(logs.String(), privateName) {
				t.Fatal("outside filename leaked to diagnostics")
			}
			for _, dir := range []string{"results", ".failed", ".processed"} {
				if got := dirEntries(t, filepath.Join(namespace, dir)); len(got) != 0 {
					t.Fatalf("hostile channel created %s entries: %v", dir, got)
				}
			}
			if data, err := os.ReadFile(outsideFile); err != nil || string(data) != `{"op":"list"}` {
				t.Fatal("outside file modified", err)
			}
		})
	}
}

func TestWatcher_ChannelFIFOOrFileDoesNotBlockDispatch(t *testing.T) {
	for _, kind := range []string{"fifo", "file"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			namespace := filepath.Join(root, "ipc", "alice")
			if err := os.MkdirAll(namespace, 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(namespace, "tasks")
			var err error
			if kind == "fifo" {
				err = syscall.Mkfifo(path, 0600)
			} else {
				err = os.WriteFile(path, []byte("not a directory"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			w := New(Config{DataDir: root}, &fakeMessageSink{}, &fakeTaskSink{}, nil, nil)
			w.processChannel(context.Background(), "alice", "tasks", func(context.Context, string, string) bool { calls++; return true })
			if calls != 0 {
				t.Fatal("invalid channel reached handler")
			}
		})
	}
}

// Pending IPC from a legacy blocked group must neither execute nor disappear.
// The same sweep continues serving the admitted personal namespace.
func TestWatcher_NamespacePolicyPreservesDeniedPendingIPC(t *testing.T) {
	root := t.TempDir()
	for _, folder := range []string{"owner", "group"} {
		writeIPC(t, root, folder, "messages", "message.json", map[string]string{"chatJid": map[string]string{"owner": "1001", "group": "2002"}[folder], "text": folder})
		writeIPC(t, root, folder, "tasks", "task.json", map[string]any{"op": "schedule", "taskId": folder + "-task", "prompt": "remind", "scheduleType": "interval", "scheduleValue": "1h"})
	}
	// More than the configured retention: denied namespaces must not be pruned.
	for _, dir := range []string{".processed", ".failed", "results"} {
		for _, name := range []string{"old-a.json", "old-b.json"} {
			path := filepath.Join(root, "ipc", "group", dir, name)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	messages, tasks := &fakeMessageSink{}, &fakeTaskSink{}
	seen := map[string]bool{}
	w := New(Config{DataDir: root, OwnerFolder: "owner", Retention: 1, NamespaceAllowed: func(ctx context.Context, folder string) bool {
		if ctx.Err() != nil {
			t.Fatal("unexpected canceled context")
		}
		seen[folder] = true
		return folder == "owner"
	}}, messages, tasks, nil, staticResolver(map[string]string{"owner": "1001", "group": "2002"}))
	w.tickOnce(context.Background())
	if !seen["owner"] || !seen["group"] {
		t.Fatalf("gate did not inspect all namespaces: %v", seen)
	}
	calls := messages.callsSnapshot()
	if len(calls) != 1 || calls[0].text != "owner" {
		t.Fatalf("message calls: %+v", calls)
	}
	if len(tasks.scheduled) != 1 || tasks.scheduled[0].ID != "owner-task" {
		t.Fatalf("task calls: %+v", tasks.scheduled)
	}
	for _, channel := range []string{"messages", "tasks"} {
		if got := dirEntries(t, filepath.Join(root, "ipc", "group", channel)); len(got) != 1 {
			t.Fatalf("denied pending %s changed: %v", channel, got)
		}
	}
	for _, dir := range []string{".processed", ".failed", "results"} {
		if got := dirEntries(t, filepath.Join(root, "ipc", "group", dir)); len(got) != 2 {
			t.Fatalf("denied %s mutated: %v", dir, got)
		}
	}
}
