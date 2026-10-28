package telegram

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/ipc"
)

type noopMessages struct{ calls int }

func (n *noopMessages) Send(context.Context, string, string) error { n.calls++; return nil }

// End to end with a fake transport and no network: an owner IPC request is
// queued, dispatched once, and the receipt follows the delivery to its outcome.
func TestOutboxIPC_ReceiptFollowsDispatcherOutcome(t *testing.T) {
	for _, tc := range []struct {
		name   string
		script func(int, SendRequest) Outcome
		want   string
	}{
		{"sent", nil, "sent"},
		{"uncertain", func(int, SendRequest) Outcome { return Outcome{Kind: OutcomeAmbiguous, Code: "timeout"} }, "uncertain"},
		{"failed", func(int, SendRequest) Outcome { return Outcome{Kind: OutcomeRefused, Code: "chat_not_found"} }, "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newOutboxFixture(t)
			tr := &fakeTransport{script: tc.script}
			f.start(f.owner, tr, fastCfg())

			root := t.TempDir()
			dir := filepath.Join(root, "ipc", "owner", "messages")
			if err := os.MkdirAll(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			req, _ := json.Marshal(map[string]string{"chatJid": "tg:1", "text": "hello"})
			if err := os.WriteFile(filepath.Join(dir, "0001.json"), req, 0o600); err != nil {
				t.Fatal(err)
			}
			legacy := &noopMessages{}
			w := ipc.New(ipc.Config{DataDir: root, OwnerFolder: "owner", Tick: 10 * time.Millisecond,
				Durable: ipc.DurableConfig{Store: f.st, PersonalJID: "tg:1"}},
				legacy, nil, slog.New(slog.NewTextHandler(io.Discard, nil)),
				func(folder string) (string, bool) { return "tg:1", folder == "owner" })
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			go func() { _ = w.Run(ctx); close(done) }()
			defer func() { cancel(); <-done }()

			receipt := filepath.Join(root, "ipc", "owner", "results", "messages", "0001.result.json")
			eventually(t, "receipt "+tc.want, func() bool {
				b, err := os.ReadFile(receipt)
				if err != nil {
					return false
				}
				var m map[string]any
				return json.Unmarshal(b, &m) == nil && m["status"] == tc.want
			})
			if legacy.calls != 0 {
				t.Fatalf("legacy sink used %d times", legacy.calls)
			}
			if got := tr.texts(); len(got) != 1 || got[0] != "hello" {
				t.Fatalf("transport calls: %v", got)
			}
		})
	}
}
