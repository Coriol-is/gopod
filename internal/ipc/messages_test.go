package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// helper: drop a JSON file under data/ipc/<folder>/<channel>/<name>.
func writeIPC(t *testing.T, root, folder, channel, name string, payload any) {
	t.Helper()
	dir := filepath.Join(root, "ipc", folder, channel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	for _, sub := range []string{".processed", ".failed"} {
		os.MkdirAll(filepath.Join(root, "ipc", folder, sub), 0o755)
	}
	b, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func dirEntries(t *testing.T, dir string) []string {
	t.Helper()
	es, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("readdir %q: %v", dir, err)
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Name())
	}
	return out
}

func TestWatcher_MessageHappyPath(t *testing.T) {
	root := t.TempDir()

	writeIPC(t, root, "alice", "messages", "0000000000001-aaaaaa.json",
		map[string]string{"chatJid": "2002", "text": "hello"})

	clk := &fakeClock{now: time.Unix(0, 0)}
	msgs := &fakeMessageSink{}

	w := New(Config{
		DataDir:     root,
		OwnerFolder: "owner",
		NowFn:       clk.Now,
	}, msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{
			"alice": "2002",
			"owner": "1001",
		}))

	w.tickOnce(context.Background())

	calls := msgs.callsSnapshot()
	if len(calls) != 1 {
		t.Fatalf("Send calls = %d, want 1", len(calls))
	}
	if calls[0].chatJID != "2002" || calls[0].text != "hello" {
		t.Errorf("Send call = %+v", calls[0])
	}

	processed := dirEntries(t, filepath.Join(root, "ipc", "alice", ".processed"))
	if len(processed) != 1 {
		t.Errorf(".processed entries = %v, want 1", processed)
	}
	pending := dirEntries(t, filepath.Join(root, "ipc", "alice", "messages"))
	if len(pending) != 0 {
		t.Errorf("messages/ entries = %v, want empty", pending)
	}
}

func TestWatcher_MalformedJSON(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "ipc", "alice", "messages")
	os.MkdirAll(dir, 0o755)
	os.MkdirAll(filepath.Join(root, "ipc", "alice", ".processed"), 0o755)
	os.MkdirAll(filepath.Join(root, "ipc", "alice", ".failed"), 0o755)
	if err := os.WriteFile(filepath.Join(dir, "0001.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	msgs := &fakeMessageSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 0 {
		t.Errorf("Send calls = %d, want 0", len(calls))
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1 entry", got)
	}
}

func TestWatcher_AuthzNonOwnerCrossChat(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "0001.json",
		map[string]string{"chatJid": "1001", "text": "evil"}) // owner's jid
	msgs := &fakeMessageSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002", "owner": "1001"}))
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 0 {
		t.Errorf("Send was called for unauthorized message")
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}

func TestWatcher_TransientRetryThenSuccess(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "0001.json",
		map[string]string{"chatJid": "2002", "text": "hi"})

	clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	msgs := &fakeMessageSink{
		responses: []error{
			errors.New("transient 1"),
			errors.New("transient 2"),
			nil, // succeed on the third attempt
		},
	}
	w := New(Config{
		DataDir: root, OwnerFolder: "owner", NowFn: clk.Now,
	}, msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))

	// Tick 1: first attempt fails, file stays, retry @ +5s.
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("after tick 1: Send calls = %d, want 1", len(calls))
	}

	// Tick 2 inside the cool-down: should be skipped, no Send call.
	clk.advance(2 * time.Second)
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 1 {
		t.Fatalf("during cool-down: Send calls = %d, want 1", len(calls))
	}

	// Tick 3 past first backoff (5s + 1s slack), 2nd attempt fails.
	clk.advance(4 * time.Second)
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 2 {
		t.Fatalf("after backoff 1: Send calls = %d, want 2", len(calls))
	}

	// Tick 4 past second backoff (15s), 3rd attempt succeeds.
	clk.advance(20 * time.Second)
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 3 {
		t.Fatalf("after backoff 2: Send calls = %d, want 3", len(calls))
	}

	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".processed")); len(got) != 1 {
		t.Errorf(".processed = %v, want 1", got)
	}
}

func TestWatcher_TransientExhausted(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "0001.json",
		map[string]string{"chatJid": "2002", "text": "hi"})

	clk := &fakeClock{now: time.Unix(1_700_000_000, 0)}
	msgs := &fakeMessageSink{
		responses: []error{
			errors.New("e1"),
			errors.New("e2"),
			errors.New("e3"),
		},
	}
	w := New(Config{
		DataDir: root, OwnerFolder: "owner", NowFn: clk.Now,
	}, msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))

	w.tickOnce(context.Background()) // attempt 1
	clk.advance(10 * time.Second)
	w.tickOnce(context.Background()) // attempt 2
	clk.advance(20 * time.Second)
	w.tickOnce(context.Background()) // attempt 3 → .failed/

	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}

func TestWatcher_PermanentSinkError(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "0001.json",
		map[string]string{"chatJid": "2002", "text": "hi"})
	msgs := &fakeMessageSink{
		responses: []error{
			fmt.Errorf("bad: %w", ErrPermanent),
		},
	}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if calls := msgs.callsSnapshot(); len(calls) != 1 {
		t.Errorf("Send calls = %d, want 1", len(calls))
	}
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}

func TestWatcher_PanicRecovered(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "0001.json",
		map[string]string{"chatJid": "2002", "text": "hi"})
	msgs := &fakeMessageSink{panic: true}
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		msgs, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background()) // must not panic out of tickOnce
	if got := dirEntries(t, filepath.Join(root, "ipc", "alice", ".failed")); len(got) != 1 {
		t.Errorf(".failed = %v, want 1", got)
	}
}

func TestWatcher_RetentionCap(t *testing.T) {
	root := t.TempDir()
	processed := filepath.Join(root, "ipc", "alice", ".processed")
	os.MkdirAll(processed, 0o755)
	for i := 0; i < 250; i++ {
		os.WriteFile(filepath.Join(processed, fmt.Sprintf("%013d-x.json", i)), []byte("{}"), 0o644)
	}
	// Empty messages/ + .failed/ to satisfy handleFolder.
	os.MkdirAll(filepath.Join(root, "ipc", "alice", "messages"), 0o755)
	os.MkdirAll(filepath.Join(root, "ipc", "alice", ".failed"), 0o755)
	w := New(Config{DataDir: root, OwnerFolder: "owner"},
		&fakeMessageSink{}, &fakeTaskSink{}, nil,
		staticResolver(map[string]string{"alice": "2002"}))
	w.tickOnce(context.Background())
	if got := dirEntries(t, processed); len(got) != 200 {
		t.Errorf(".processed = %d, want 200", len(got))
	}
}

func TestWatcher_MessageReceipts(t *testing.T) {
	for _, tc := range []struct {
		name, jid, text, status string
		sinkErr                 error
	}{
		{name: "success", jid: "tg:2002", text: "hello", status: "ok"},
		{name: "unauthorized", jid: "tg:1001", text: "hello", status: "error"},
		{name: "empty", jid: "tg:2002", status: "error"},
		{name: "permanent", jid: "tg:2002", text: "hello", status: "error", sinkErr: ErrPermanent},
		{name: "transient", jid: "tg:2002", text: "hello", status: "retrying", sinkErr: errors.New("network failure")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			writeIPC(t, root, "alice", "messages", "probe.json", map[string]string{"chatJid": tc.jid, "text": tc.text})
			sink := &fakeMessageSink{responses: []error{tc.sinkErr}}
			w := New(Config{DataDir: root, OwnerFolder: "owner"}, sink, &fakeTaskSink{}, nil, staticResolver(map[string]string{"alice": "tg:2002", "owner": "tg:1001"}))
			w.tickOnce(context.Background())
			data, err := os.ReadFile(filepath.Join(root, "ipc", "alice", "results", "messages", "probe.result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var r struct{ Status, Error string }
			if err = json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.Status != tc.status {
				t.Fatalf("receipt=%s", data)
			}
			if tc.status != "ok" && r.Error == "" {
				t.Fatalf("reason missing: %s", data)
			}
			if tc.name == "unauthorized" && len(sink.callsSnapshot()) != 0 {
				t.Fatal("cross-chat delivery")
			}
			if _, err := os.Stat(filepath.Join(root, "ipc", "owner", "results")); !os.IsNotExist(err) {
				t.Fatal("receipt crossed source namespace")
			}
		})
	}
}

func TestWatcher_MessageReceiptFailureDoesNotResend(t *testing.T) {
	root := t.TempDir()
	writeIPC(t, root, "alice", "messages", "probe.json", map[string]string{"chatJid": "tg:2002", "text": "hello"})
	if err := os.WriteFile(filepath.Join(root, "ipc", "alice", "results"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	sink := &fakeMessageSink{}
	w := New(Config{DataDir: root, OwnerFolder: "owner"}, sink, &fakeTaskSink{}, nil, staticResolver(map[string]string{"alice": "tg:2002"}))
	w.tickOnce(context.Background())
	w.tickOnce(context.Background())
	if len(sink.callsSnapshot()) != 1 {
		t.Fatal("receipt write failure resent a delivered message")
	}
	if _, err := os.Stat(filepath.Join(root, "ipc", "alice", ".processed", "probe.json")); err != nil {
		t.Fatal(err)
	}
}

func TestWatcher_MessageRejectsOversizedAndSymlink(t *testing.T) {
	for _, kind := range []string{"oversized", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			writeIPC(t, root, "alice", "messages", "probe.json", map[string]string{"chatJid": "tg:2002", "text": "hello"})
			path := filepath.Join(root, "ipc", "alice", "messages", "probe.json")
			if kind == "oversized" {
				if err := os.WriteFile(path, make([]byte, (64<<10)+1), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				outside := filepath.Join(t.TempDir(), "message.json")
				if err := os.WriteFile(outside, []byte(`{"chatJid":"tg:2002","text":"escaped"}`), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(outside, path); err != nil {
					t.Fatal(err)
				}
			}
			sink := &fakeMessageSink{}
			w := New(Config{DataDir: root, OwnerFolder: "owner"}, sink, &fakeTaskSink{}, nil, staticResolver(map[string]string{"alice": "tg:2002"}))
			w.tickOnce(context.Background())
			if len(sink.callsSnapshot()) != 0 {
				t.Fatal("invalid file dispatched")
			}
			data, err := os.ReadFile(filepath.Join(root, "ipc", "alice", "results", "messages", "probe.result.json"))
			if err != nil {
				t.Fatal(err)
			}
			var r struct{ Status, Error string }
			if err = json.Unmarshal(data, &r); err != nil {
				t.Fatal(err)
			}
			if r.Status != "error" || r.Error == "" {
				t.Fatalf("receipt=%s", data)
			}
		})
	}
}

func TestWatcher_MessageArchiveFailureDoesNotResend(t *testing.T) {
	for _, partial := range []bool{false, true} {
		t.Run(fmt.Sprint(partial), func(t *testing.T) {
			root := t.TempDir()
			writeIPC(t, root, "alice", "messages", "probe.json", map[string]string{"chatJid": "tg:2002", "text": "hello"})
			archive := ".processed"
			sink := &fakeMessageSink{}
			if partial {
				archive = ".failed"
				sink.responses = []error{fmt.Errorf("partial: %w", ErrPermanent)}
			}
			blocked := filepath.Join(root, "ipc", "alice", archive)
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(blocked, []byte("blocked"), 0600); err != nil {
				t.Fatal(err)
			}
			w := New(Config{DataDir: root, OwnerFolder: "owner"}, sink, &fakeTaskSink{}, nil, staticResolver(map[string]string{"alice": "tg:2002"}))
			w.processMessageFile(context.Background(), "alice", filepath.Join(root, "ipc", "alice", "messages", "probe.json"))
			w.processMessageFile(context.Background(), "alice", filepath.Join(root, "ipc", "alice", "messages", "probe.json"))
			if len(sink.callsSnapshot()) != 1 {
				t.Fatal("archive failure resent terminal message")
			}
			if err := os.Remove(blocked); err != nil {
				t.Fatal(err)
			}
			w.processMessageFile(context.Background(), "alice", filepath.Join(root, "ipc", "alice", "messages", "probe.json"))
			if len(sink.callsSnapshot()) != 1 || len(w.terminalMessages) != 0 {
				t.Fatal("archive recovery resent message or leaked terminal outcome")
			}
			if _, err := os.Stat(filepath.Join(blocked, "probe.json")); err != nil {
				t.Fatal(err)
			}
		})
	}
}
