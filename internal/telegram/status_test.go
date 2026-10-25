package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-telegram/bot"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// manualClock is a manual reactClock: timers fire, in due order and on
// the caller's goroutine, only when Advance passes their time.
type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	seq    int
	timers map[int]fakeTimer
}

type fakeTimer struct {
	at  time.Time
	seq int
	f   func()
}

func newManualClock() *manualClock {
	return &manualClock{now: time.Unix(1_000_000, 0), timers: map[int]fakeTimer{}}
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) AfterFunc(d time.Duration, f func()) func() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seq++
	id := c.seq
	c.timers[id] = fakeTimer{at: c.now.Add(d), seq: id, f: f}
	return func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		_, ok := c.timers[id]
		delete(c.timers, id)
		return ok
	}
}

// Advance moves the clock by d, firing every timer that falls due
// (including timers those timers set) in order.
func (c *manualClock) Advance(d time.Duration) {
	c.mu.Lock()
	end := c.now.Add(d)
	c.mu.Unlock()
	for {
		c.mu.Lock()
		var due []fakeTimer
		for _, t := range c.timers {
			if !t.at.After(end) {
				due = append(due, t)
			}
		}
		if len(due) == 0 {
			c.now = end
			c.mu.Unlock()
			return
		}
		sort.Slice(due, func(i, j int) bool {
			if due[i].at.Equal(due[j].at) {
				return due[i].seq < due[j].seq
			}
			return due[i].at.Before(due[j].at)
		})
		t := due[0]
		delete(c.timers, t.seq)
		if t.at.After(c.now) {
			c.now = t.at
		}
		c.mu.Unlock()
		t.f()
	}
}

// reactorFixture is a reactor whose writes are recorded.
type reactorFixture struct {
	clock    *manualClock
	r        *statusReactor
	mu       sync.Mutex
	writes   []string
	fail     error // returned by every write while set
	released bool
}

func newReactorFixture() *reactorFixture {
	f := &reactorFixture{clock: newManualClock()}
	f.r = &statusReactor{
		clock: f.clock,
		log:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		write: func(emoji string) error {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.writes = append(f.writes, emoji)
			return f.fail
		},
		release: func() {
			f.mu.Lock()
			f.released = true
			f.mu.Unlock()
		},
	}
	return f
}

func (f *reactorFixture) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.writes...)
}

func TestStatusReactor_DebouncesBursts(t *testing.T) {
	f := newReactorFixture()
	f.r.Set(stQueued)
	f.clock.Advance(0)
	if got := f.got(); len(got) != 1 || got[0] != "👀" {
		t.Fatalf("first state is written at once: %q", got)
	}
	// A burst inside the window: thinking, tool, thinking, tool.
	f.clock.Advance(100 * time.Millisecond)
	f.r.Set(stThinking)
	f.r.Set(stTool)
	f.r.Set(stThinking)
	f.r.Set(stTool)
	f.clock.Advance(500 * time.Millisecond) // 600 ms after the first write
	if got := f.got(); len(got) != 1 {
		t.Fatalf("no write inside the debounce window: %q", got)
	}
	f.clock.Advance(100 * time.Millisecond)
	got := f.got()
	if len(got) != 2 || got[1] != "👨‍💻" {
		t.Fatalf("one write with the latest state after the window: %q", got)
	}
	// Setting the state already shown costs nothing.
	f.clock.Advance(time.Second)
	f.r.Set(stTool)
	f.clock.Advance(time.Second)
	if n := len(f.got()); n != 2 {
		t.Fatalf("unchanged state must not be re-sent: %d writes", n)
	}
}

func TestStatusReactor_FinalStateAlwaysWritten(t *testing.T) {
	f := newReactorFixture()
	f.r.Set(stThinking)
	f.clock.Advance(0)
	f.r.Set(stTool) // debounced: pending
	f.r.Set(stDone) // final: at once, inside the window
	got := f.got()
	if len(got) != 2 || got[1] != "👍" {
		t.Fatalf("final state must be written at once: %q", got)
	}
	f.clock.Advance(time.Minute)
	f.r.Set(stTool) // after the end: ignored
	f.r.Touch()
	f.clock.Advance(2 * time.Minute)
	if got := f.got(); len(got) != 2 {
		t.Fatalf("nothing after the final state (no stale tool, no stall): %q", got)
	}
	if !f.released {
		t.Error("the reactor is released after its final write")
	}
}

func TestStatusReactor_FinalRetriedAfter429(t *testing.T) {
	f := newReactorFixture()
	f.fail = &bot.TooManyRequestsError{Message: "Too Many Requests", RetryAfter: 2}
	f.r.Set(stError)
	f.mu.Lock()
	f.fail = nil
	f.mu.Unlock()
	f.clock.Advance(2 * time.Second)
	got := f.got()
	if len(got) != 2 || got[1] != "😱" {
		t.Fatalf("a final state refused with 429 is retried: %q", got)
	}
}

func TestStatusReactor_StallAfter25s(t *testing.T) {
	f := newReactorFixture()
	f.r.Set(stThinking)
	f.clock.Advance(24 * time.Second)
	f.r.Touch() // progress: the stall clock restarts
	f.clock.Advance(24 * time.Second)
	if got := f.got(); len(got) != 1 {
		t.Fatalf("no stall while progress arrives: %q", got)
	}
	f.clock.Advance(time.Second) // 25 s since the last progress
	got := f.got()
	if len(got) != 2 || got[1] != "🥱" {
		t.Fatalf("soft stall after 25 s: %q", got)
	}
	f.clock.Advance(35 * time.Second) // 60 s since the last progress
	got = f.got()
	if len(got) != 3 || got[2] != "😨" {
		t.Fatalf("hard stall after 60 s: %q", got)
	}
	f.r.Touch() // progress again: back to the active state
	f.clock.Advance(time.Second)
	got = f.got()
	if len(got) != 4 || got[3] != "🤔" {
		t.Fatalf("progress after a stall restores the state: %q", got)
	}
}

func TestStatusReactor_QueuedDoesNotStall(t *testing.T) {
	f := newReactorFixture()
	f.r.Set(stQueued)
	f.r.Touch()
	f.clock.Advance(5 * time.Minute)
	if got := f.got(); len(got) != 1 {
		t.Fatalf("a queued message must not stall: %q", got)
	}
}

func TestStatusReactor_RefusedStopsWriting(t *testing.T) {
	f := newReactorFixture()
	f.fail = errors.New("Bad Request: not enough rights to set reactions")
	f.r.Set(stQueued)
	f.clock.Advance(0)
	f.mu.Lock()
	f.fail = nil
	f.mu.Unlock()
	f.r.Set(stThinking)
	f.clock.Advance(time.Second)
	f.r.Set(stDone)
	if got := f.got(); len(got) != 1 {
		t.Fatalf("a message that refused a reaction gets no more writes: %q", got)
	}
}

func TestStatusReactor_NilIsNoop(t *testing.T) {
	var r *statusReactor
	r.Set(stDone)
	r.Touch()
	b := &Bot{}
	if b.newStatusReactor(1, 0) != nil {
		t.Error("no message id: no reactor")
	}
}

// telegramReactionEmoji is the ReactionTypeEmoji list of the Bot API
// (https://core.telegram.org/bots/api#reactiontypeemoji), copied as is.
var telegramReactionEmoji = strings.Fields(`👍 👎 ❤ 🔥 🥰 👏 😁 🤔 🤯 😱 🤬 😢 🎉 🤩 🤮 💩 🙏 👌 🕊 🤡 🥱 🥴 😍 🐳 ❤‍🔥 🌚 🌭 💯 🤣 ⚡ 🍌 🏆 💔 🤨 😐 🍓 🍾 💋 🖕 😈 😴 😭 🤓 👻 👨‍💻 👀 🎃 🙈 😇 😨 🤝 ✍ 🤗 🫡 🎅 🎄 ☃ 💅 🤪 🗿 🆒 💘 🙉 🦄 😘 💊 🙊 😎 👾 🤷‍♂ 🤷 🤷‍♀ 😡`)

func TestStatusEmojiAllAllowed(t *testing.T) {
	allowed := map[string]bool{}
	for _, e := range telegramReactionEmoji {
		allowed[e] = true
	}
	for s, e := range stateEmoji {
		if !allowed[e] {
			t.Errorf("state %d: %q is not an allowed reaction", s, e)
		}
	}
	for _, e := range []string{emojiThinking, emojiDone, emojiError} {
		if !allowed[e] {
			t.Errorf("%q is not an allowed reaction", e)
		}
	}
	if len(stateEmoji) != int(stCompacting) {
		t.Errorf("every state but stNone needs an emoji: %d of %d", len(stateEmoji), stCompacting)
	}
}

// A chat that refuses reactions (no rights in a group) still gets its
// reply: reaction errors are only logged.
func TestStatusReactions_ReactionErrorsNeverFailTurn(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "setMessageReaction":
			return apiError(http.StatusBadRequest, "Bad Request: REACTION_INVALID")
		case "sendMessage":
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()
	s.finish("answer")
	if err := <-done; err != nil {
		t.Fatalf("a refused reaction must not fail the turn: %v", err)
	}
	if len(fake.callsTo("sendMessage")) == 0 {
		t.Fatal("the reply still goes out")
	}
}

// reactionsOf returns the emoji of every setMessageReaction call.
func reactionsOf(fake *fakeTelegram) []string {
	var out []string
	for _, c := range fake.callsTo("setMessageReaction") {
		for _, e := range stateEmoji {
			if strings.Contains(c.Form["reaction"], `"`+e+`"`) {
				out = append(out, e)
			}
		}
	}
	return out
}

// A streamed turn with a tool step moves the user's message through
// thinking and tool and always ends on done, with no 429-prone burst:
// a quick turn costs at most one write per distinct state.
func TestStatusReactions_StreamedTurnEndsDone(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	b.turnStatus(item).Set(stThinking)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()
	for i := 0; i < 10; i++ {
		s.progress <- runner.ProgressEvent{Tool: "Grep", Detail: "p"}
		s.chunks <- "narration "
	}
	s.finish("answer")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got := reactionsOf(fake)
	if len(got) == 0 || got[len(got)-1] != "👍" {
		t.Fatalf("the turn ends on done: %q", got)
	}
	if len(got) > 3 {
		t.Fatalf("20 state changes inside one window must be debounced: %q", got)
	}
}

func TestStatusReactions_StoppedTurn(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	s := newStructuredStream()
	if err := b.finishStopped(t.Context(), item, s.stream(), "half"); !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	s.finish("")
	if got := reactionsOf(fake); len(got) != 1 || got[0] != "👌" {
		t.Fatalf("a stopped turn ends on stopped: %q", got)
	}
}

// A final state refused with a long 429 is given up, and the reactor
// released: a new turn on the same message gets a fresh one.
func TestStatusReactor_LostFinalReleases(t *testing.T) {
	fake := &fakeTelegram{}
	limited := true
	var mu sync.Mutex
	fake.respond = func(method string, _ map[string]string) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if method == "setMessageReaction" && limited {
			return apiError(http.StatusTooManyRequests, "Too Many Requests: retry after 30")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b := newFakeBot(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	old := b.newStatusReactor(streamChat, 10)
	old.Set(stError)
	mu.Lock()
	limited = false
	mu.Unlock()
	fresh := b.newStatusReactor(streamChat, 10)
	if fresh == old {
		t.Fatal("a finished reactor must not be reused by the next turn")
	}
	fresh.Set(stQueued)
	clock.Advance(0)
	if got := reactionsOf(fake); len(got) != 2 || got[1] != "👀" {
		t.Fatalf("the new turn's state is written: %q", got)
	}
}

// A late queued state (a Retry's, racing the worker) never sets a
// running message back to 👀.
func TestStatusReactor_LateQueuedIgnored(t *testing.T) {
	f := newReactorFixture()
	f.r.Set(stThinking)
	f.clock.Advance(0)
	f.r.Set(stQueued)
	f.clock.Advance(time.Second)
	if got := f.got(); len(got) != 1 || got[0] != "🤔" {
		t.Fatalf("queued after thinking: %q", got)
	}
}

// /compact answered with a logical error (Code != 0, nil error) ends on
// the error reaction, not done.
func TestCompactLogicalErrorIsNotDone(t *testing.T) {
	fake := threadFake()
	b, _ := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	r := control.New(b.log)
	control.RegisterSessionCommands(r,
		func(context.Context, string, int, bool) (string, error) { return "", nil },
		func(int64) string { return "" }) // not registered
	b.router = r
	b.dispatchSlash(t.Context(), threadedMsg(0, "/compact"), "/compact")
	got := reactionsOf(fake)
	if len(got) == 0 || got[len(got)-1] != "😱" {
		t.Fatalf("reactions = %q, want ending on 😱", got)
	}
	for _, e := range got {
		if e == "👍" {
			t.Fatalf("a refused /compact got done: %q", got)
		}
	}
}

// Retry's 👀 goes through the reactor.
func TestRetryReactsQueuedThroughReactor(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	withQueue(t, b)
	id := failedTurn(t, b, streamChat)
	b.callbackHandler(t.Context(), b.api, cbUpdate("q1", streamChat, 9, 99, fmt.Sprintf("rt:%d", id)))
	waitFor(t, "queued reaction", func() bool {
		got := reactionsOf(fake)
		return len(got) == 1 && got[0] == "👀"
	})
	b.statusMu.Lock()
	r := b.statuses[statusKey{streamChat, 20}]
	b.statusMu.Unlock()
	if r == nil {
		t.Fatal("the retried message has a reactor")
	}
	r.Set(stThinking)
	r.Set(stQueued) // nothing moves it back
	r.mu.Lock()
	want := r.want
	r.mu.Unlock()
	if want != stThinking {
		t.Errorf("want = %d after a late queued", want)
	}
}
