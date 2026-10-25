package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// memTurns is an in-memory queue.TurnStore that dedups like the turns
// table's UNIQUE(source, source_id).
type memTurns struct {
	mu    sync.Mutex
	items []queue.Item
	seen  map[string]bool
	fail  bool // InsertTurn fails: the turn is not saved
}

func (m *memTurns) InsertTurn(_ context.Context, it queue.Item) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return 0, false, errors.New("disk full")
	}
	if m.seen == nil {
		m.seen = map[string]bool{}
	}
	key := it.Source + "|" + it.SourceID
	if m.seen[key] {
		return 0, true, nil
	}
	m.seen[key] = true
	m.items = append(m.items, it)
	return int64(1000 + len(m.items)), false, nil
}
func (m *memTurns) MarkRunning(context.Context, int64) error                  { return nil }
func (m *memTurns) MarkFinished(context.Context, int64, string, string) error { return nil }
func (m *memTurns) LoadUnfinished(context.Context) ([]queue.Item, error)      { return nil, nil }

func (m *memTurns) inserted() []queue.Item {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]queue.Item(nil), m.items...)
}

// withQueue gives b a durable-looking queue whose turns land in the
// returned memTurns; the handler does nothing.
func withQueue(t *testing.T, b *Bot) *memTurns {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mt := &memTurns{}
	b.queue = queue.New(ctx, func(context.Context, []queue.Item) error { return nil }, 1, mt,
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	return mt
}

// fakeClock makes b.now return *at.
func fakeClock(b *Bot, at *time.Time) { b.now = func() time.Time { return *at } }

func errorSends(fake *fakeTelegram) []apiCall { return fake.callsTo("sendMessage") }

func TestClassifyErr(t *testing.T) {
	cases := []struct {
		err  error
		want errKind
	}{
		{fmt.Errorf("run: %w", runner.ErrSessionExpired), errAuthExpired},
		{fmt.Errorf("run: %w", runner.ErrNotLoggedIn), errNotLoggedIn},
		{fmt.Errorf("x: %w", errReplyUndelivered), errDelivery},
		{errors.New("exit status 1"), errAgentFailed},
	}
	for _, c := range cases {
		if got := classifyErr(c.err); got != c.want {
			t.Errorf("classifyErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}

func TestReplyError_AuthExpiredOffersLogin(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7

	b.replyError(context.Background(), item, fmt.Errorf("run: %w", runner.ErrSessionExpired))

	sends := errorSends(fake)
	if len(sends) != 1 || sends[0].Form["text"] != errTextAuthExpired {
		t.Fatalf("sends = %+v", sends)
	}
	if data, _ := markupData(t, sends[0]); len(data) != 1 || data[0] != "lg" {
		t.Fatalf("buttons = %v, want the login button", data)
	}
	if sends[0].Form["message_thread_id"] != "7" || !strings.Contains(sends[0].Form["reply_parameters"], `"message_id":10`) {
		t.Fatalf("not a reply in the turn's topic: %v", sends[0].Form)
	}
	if r := fake.callsTo("setMessageReaction"); len(r) != 1 || !strings.Contains(r[0].Form["reaction"], emojiError) {
		t.Fatalf("reactions = %+v", r)
	}
}

func TestReplyError_AgentFailedOffersRetry(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	withQueue(t, b)

	b.replyError(context.Background(), item, errors.New("exit status 1"))

	sends := errorSends(fake)
	if len(sends) != 1 || sends[0].Form["text"] != errTextAgentFailed {
		t.Fatalf("sends = %+v", sends)
	}
	want := fmt.Sprintf("rt:%d", item.ID)
	if data, _ := markupData(t, sends[0]); len(data) != 1 || data[0] != want {
		t.Fatalf("buttons = %v, want [%s]", data, want)
	}
}

// Without a durable turn (no queue, or a memory-only turn) there is
// nothing to retry: the reply has no button.
func TestReplyError_NoRetryWithoutTurn(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)

	b.replyError(context.Background(), item, errors.New("exit status 1"))

	if data, _ := markupData(t, errorSends(fake)[0]); len(data) != 0 {
		t.Fatalf("buttons = %v, want none", data)
	}
}

func TestReplyError_CooldownSuppressesSecondWithin60s(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	now := time.Unix(1_000_000, 0)
	fakeClock(b, &now)
	ctx := context.Background()
	boom := errors.New("exit status 1")

	b.replyError(ctx, item, boom)
	now = now.Add(59 * time.Second)
	b.replyError(ctx, item, boom)
	if n := len(errorSends(fake)); n != 1 {
		t.Fatalf("sends within 60 s = %d, want 1", n)
	}
	if n := len(fake.callsTo("setMessageReaction")); n != 2 {
		t.Fatalf("reactions = %d, want 2 (a repeat still changes the reaction)", n)
	}

	// Another kind, and another chat, are not suppressed.
	b.replyError(ctx, item, runner.ErrNotLoggedIn)
	other := item
	other.ChatID = streamChat + 1
	b.replyError(ctx, other, boom)
	if n := len(errorSends(fake)); n != 3 {
		t.Fatalf("sends = %d, want 3", n)
	}

	now = now.Add(2 * time.Second) // 61 s after the first
	b.replyError(ctx, item, boom)
	if n := len(errorSends(fake)); n != 4 {
		t.Fatalf("sends after the cooldown = %d, want 4", n)
	}
}

func TestReplyError_NoStderrInText(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	leak := "claude: exit status 1: stderr: open /home/node/.claude/creds.json: token=sk-ant-SECRET"

	b.replyError(context.Background(), item, errors.New(leak))
	b.replyError(context.Background(), item, fmt.Errorf("%s: %w", leak, runner.ErrSessionExpired))

	for _, s := range errorSends(fake) {
		text := s.Form["text"]
		for _, bad := range []string{"stderr", "/home", "token", "SECRET", "exit status", "claude:"} {
			if strings.Contains(text, bad) {
				t.Fatalf("error reply leaks %q: %q", bad, text)
			}
		}
	}
}

// The stream's error path replies through the table.
func TestStreamErrorUsesHumaneReply(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(streamChat)
	s := newStructuredStream()
	go func() {
		close(s.progress)
		s.result <- runner.RunResult{Err: errors.New("exit status 1: stderr: boom")}
		close(s.result)
		close(s.chunks)
	}()
	if err := b.streamReply(context.Background(), item, s.stream()); err != nil {
		t.Fatal(err)
	}
	sends := errorSends(fake)
	if last := sends[len(sends)-1]; last.Form["text"] != errTextAgentFailed {
		t.Fatalf("error reply = %q", last.Form["text"])
	}
}
