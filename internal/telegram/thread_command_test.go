package telegram

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/Coriol-is/gopod/internal/control"
)

// commandBot is a fake-API bot whose Router has the builtins and
// /clear, /compact wired to record the conversation they act on.
func commandBot(t *testing.T, fake *fakeTelegram) (*Bot, func() []int) {
	t.Helper()
	b, _ := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	r := control.New(slog.New(slog.NewTextHandler(io.Discard, nil)))
	control.RegisterBuiltins(r)
	r.Register("chats.list", "chats", "owner only", control.PermOwnerOnly,
		func(context.Context, control.Command) (control.Response, error) {
			return control.Response{Text: "chats"}, nil
		})
	var mu sync.Mutex
	var threads []int
	control.RegisterSessionCommands(r,
		func(_ context.Context, folder string, thread int, _ bool) (string, error) {
			mu.Lock()
			defer mu.Unlock()
			threads = append(threads, thread)
			return "summary", nil
		},
		func(int64) string { return "owner" })
	b.router = r
	b.registerCallbacks()
	return b, func() []int {
		mu.Lock()
		defer mu.Unlock()
		return append([]int(nil), threads...)
	}
}

func TestClearInThreadCallsClearWithThread(t *testing.T) {
	for _, cmd := range []string{"/clear", "/compact"} {
		for _, thread := range []int{0, 7} {
			fake := threadFake()
			b, threads := commandBot(t, fake)
			b.dispatchSlash(context.Background(), threadedMsg(thread, cmd), cmd)
			if cmd == "/clear" {
				// /clear asks first (D028): confirm on the prompt, which
				// sits in the same topic.
				if got := threads(); len(got) != 0 {
					t.Fatalf("/clear acted before confirmation: %v", got)
				}
				yes := buttonsOf(t, lastCall(t, fake, "sendMessage"))[0]
				b.callbackHandler(context.Background(), b.api,
					cbUpdate("q", streamChat, thread, 555, yes.CallbackData))
			}
			if got := threads(); len(got) != 1 || got[0] != thread {
				t.Errorf("%s in thread %d acted on %v, want [%d]", cmd, thread, got, thread)
			}
		}
	}
}

func TestCommandReplyGoesToSameThread(t *testing.T) {
	cases := []struct {
		text string
		want string // substring of the reply
	}{
		{"/ping", "pong"},
		{"/help", "gopod commands"},
		{"/clear", "Clear this conversation"}, // asks first (D028)
		{"/compact", "Session compacted"},
		{"/nope", "unknown command"},
	}
	for _, c := range cases {
		t.Run(c.text, func(t *testing.T) {
			fake := threadFake()
			b, _ := commandBot(t, fake)
			b.dispatchSlash(context.Background(), threadedMsg(7, c.text), c.text)
			sends := fake.callsTo("sendMessage")
			if len(sends) != 1 {
				t.Fatalf("sendMessage calls = %d, want 1", len(sends))
			}
			if got := sends[0].Form["message_thread_id"]; got != "7" {
				t.Errorf("reply message_thread_id = %q, want 7", got)
			}
			if !strings.Contains(sends[0].Form["text"], c.want) {
				t.Errorf("reply %q does not contain %q", sends[0].Form["text"], c.want)
			}
		})
	}

	// Authorization still comes from the Router: a non-owner chat is
	// refused an owner-only command, and the refusal lands in its thread.
	t.Run("owner-only refused in thread", func(t *testing.T) {
		fake := threadFake()
		b, _ := commandBot(t, fake)
		b.ownerChatID = 1 // the calling chat is not the owner
		b.dispatchSlash(context.Background(), threadedMsg(7, "/chats"), "/chats")
		sends := fake.callsTo("sendMessage")
		if len(sends) != 1 || sends[0].Form["text"] != "Not authorized." {
			t.Fatalf("want one \"Not authorized.\" reply, got %+v", sends)
		}
		if got := sends[0].Form["message_thread_id"]; got != "7" {
			t.Errorf("refusal message_thread_id = %q, want 7", got)
		}
	})
}
