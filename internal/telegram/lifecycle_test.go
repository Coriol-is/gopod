package telegram

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const lcOwner = int64(1001)

func TestResumeLinePlural(t *testing.T) {
	for n, want := range map[int]string{
		1:   "Продолжаю 1 незавершённый запрос.",
		2:   "Продолжаю 2 незавершённых запроса.",
		4:   "Продолжаю 4 незавершённых запроса.",
		5:   "Продолжаю 5 незавершённых запросов.",
		11:  "Продолжаю 11 незавершённых запросов.",
		12:  "Продолжаю 12 незавершённых запросов.",
		14:  "Продолжаю 14 незавершённых запросов.",
		21:  "Продолжаю 21 незавершённый запрос.",
		22:  "Продолжаю 22 незавершённых запроса.",
		25:  "Продолжаю 25 незавершённых запросов.",
		111: "Продолжаю 111 незавершённых запросов.",
		112: "Продолжаю 112 незавершённых запросов.",
		123: "Продолжаю 123 незавершённых запроса.",
	} {
		if got := resumeLine(n); got != want {
			t.Errorf("resumeLine(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestStartText(t *testing.T) {
	for _, tc := range []struct {
		kind     startKind
		replayed int
		want     string
	}{
		{startGraceful, 0, "✅ Снова на связи (версия v1)."},
		{startGraceful, 3, "✅ Снова на связи (версия v1). Продолжаю 3 незавершённых запроса."},
		{startCrash, 0, "⚠️ Был неожиданный перезапуск (версия v1)."},
		{startCrash, 1, "⚠️ Был неожиданный перезапуск (версия v1). Продолжаю 1 незавершённый запрос."},
	} {
		if got := startText(tc.kind, "v1", tc.replayed); got != tc.want {
			t.Errorf("startText(%v, %d) = %q, want %q", tc.kind, tc.replayed, got, tc.want)
		}
	}
}

func TestDecideStart(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	never := time.Time{}
	for _, tc := range []struct {
		name                  string
		prevStarted, graceful bool
		lastNotice            time.Time
		wantKind              startKind
		wantSend              bool
	}{
		{"first start ever", false, false, never, startFirst, false},
		{"graceful restart", true, true, never, startGraceful, true},
		{"crash", true, false, never, startCrash, true},
		{"graceful without start record", false, true, never, startGraceful, true},
		{"graceful inside spam window", true, true, now.Add(-time.Minute), startGraceful, false},
		{"crash inside spam window", true, false, now.Add(-119 * time.Second), startCrash, false},
		{"crash at window edge", true, false, now.Add(-2 * time.Minute), startCrash, true},
		{"graceful after window", true, true, now.Add(-time.Hour), startGraceful, true},
		{"first start ignores old notice", false, false, now.Add(-time.Second), startFirst, false},
	} {
		kind, send := decideStart(tc.prevStarted, tc.graceful, tc.lastNotice, now)
		if kind != tc.wantKind || send != tc.wantSend {
			t.Errorf("%s: decideStart = (%v, %v), want (%v, %v)", tc.name, kind, send, tc.wantKind, tc.wantSend)
		}
	}
}

// lcFixture is a fake-API bot with a real store and a Lifecycle on a
// settable clock.
type lcFixture struct {
	fake *fakeTelegram
	b    *Bot
	st   *store.Store
	lc   *Lifecycle
	cfg  LifecycleConfig
	now  time.Time
}

// newRun replaces the Lifecycle with a fresh one on the same store and
// clock, as a restarted process would have.
func (f *lcFixture) newRun() {
	f.lc = f.b.NewLifecycle(f.cfg)
	f.lc.now = func() time.Time { return f.now }
}

func newLCFixture(t *testing.T, fake *fakeTelegram, cfg LifecycleConfig) *lcFixture {
	t.Helper()
	if fake.respond == nil {
		fake.respond = func(method string, _ map[string]string) (int, string) {
			if method == "sendMessage" {
				return sentMessage("1")
			}
			return http.StatusOK, `{"ok":true,"result":true}`
		}
	}
	f := &lcFixture{fake: fake, b: newFakeBot(t, fake)}
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	f.st = st
	f.b.store = st
	cfg.Store = st
	if cfg.Version == "" {
		cfg.Version = "v1"
	}
	f.cfg = cfg
	f.now = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	f.newRun()
	return f
}

func (f *lcFixture) turn(t *testing.T, chatID int64, thread int, msgID int64, finished bool) {
	t.Helper()
	ctx := context.Background()
	id, _, err := f.st.InsertTurn(ctx, store.Turn{
		Source: "telegram", SourceID: strings.Join([]string{itoa(chatID), itoa(msgID)}, ":"),
		ChatFolder: "c" + itoa(chatID), ChatID: chatID, TGMessageID: msgID, ThreadID: thread, Text: "hi",
	})
	if err != nil {
		t.Fatalf("InsertTurn: %v", err)
	}
	if finished {
		if err := f.st.MarkTurnFinished(ctx, id, store.TurnDone, ""); err != nil {
			t.Fatalf("MarkTurnFinished: %v", err)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// sends returns "chat/thread: text" for every sendMessage call.
func (f *lcFixture) sends() []string {
	var out []string
	for _, c := range f.fake.callsTo("sendMessage") {
		th := c.Form["message_thread_id"]
		if th == "" {
			th = "0"
		}
		out = append(out, c.Form["chat_id"]+"/"+th+": "+c.Form["text"])
	}
	return out
}

func equalSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	m := map[string]int{}
	for _, s := range a {
		m[s]++
	}
	for _, s := range b {
		m[s]--
		if m[s] < 0 {
			return false
		}
	}
	return true
}

func TestShutdownNotifiesOwnerAndUnfinishedConversations(t *testing.T) {
	f := newLCFixture(t, &fakeTelegram{}, LifecycleConfig{Enabled: true, OwnerChatID: lcOwner})
	f.turn(t, 2002, 0, 1, false)    // pending, main
	f.turn(t, 2002, 0, 2, false)    // same conversation: one notice
	f.turn(t, 3003, 5, 1, false)    // thread turn: notice into thread 5
	f.turn(t, 4004, 0, 1, true)     // finished: no notice
	f.turn(t, lcOwner, 0, 9, false) // owner main: deduplicated with owner notice
	if _, _, err := f.st.InsertTurn(context.Background(), store.Turn{
		Source: "task", SourceID: "7:1", ChatFolder: "c2002", Text: "task",
	}); err != nil { // task turn, no chat id: silent
		t.Fatalf("InsertTurn task: %v", err)
	}

	f.lc.Shutdown(context.Background())

	want := []string{
		"1001/0: " + noticeShutdown,
		"2002/0: " + noticeShutdown,
		"3003/5: " + noticeShutdown,
	}
	if got := f.sends(); !equalSet(got, want) {
		t.Fatalf("sends = %q, want %q", got, want)
	}
	for _, c := range f.fake.callsTo("sendMessage") {
		if c.Form["parse_mode"] != "" {
			t.Errorf("notice sent with parse_mode %q, want plain text", c.Form["parse_mode"])
		}
	}
	if v, _ := f.st.GetState(context.Background(), stateGracefulShutdownAt); v == "" {
		t.Error("graceful shutdown marker not written")
	}
}

func TestShutdownSlowAPIBoundedAndConcurrent(t *testing.T) {
	release := make(chan struct{})
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		<-release
		return sentMessage("1")
	}}
	f := newLCFixture(t, fake, LifecycleConfig{Enabled: true, OwnerChatID: lcOwner, ShutdownTimeout: 300 * time.Millisecond})
	t.Cleanup(func() { close(release) }) // runs before the fake server closes
	f.turn(t, 2002, 0, 1, false)
	f.turn(t, 3003, 4, 1, false)

	start := time.Now()
	f.lc.Shutdown(context.Background())
	if d := time.Since(start); d > time.Second {
		t.Fatalf("Shutdown took %v with a hung API, want about the 300ms timeout", d)
	}
	// Every target was attempted although none answered: the sends
	// run concurrently, not one after another.
	if n := len(f.fake.callsTo("sendMessage")); n != 3 {
		t.Fatalf("sendMessage calls = %d, want 3 concurrent attempts", n)
	}
	if v, _ := f.st.GetState(context.Background(), stateGracefulShutdownAt); v == "" {
		t.Error("graceful shutdown marker not written when sends hang")
	}
}

func TestShutdownNoticeThreadGoneGoesToMain(t *testing.T) {
	f := newLCFixture(t, goneFake(), LifecycleConfig{Enabled: true, OwnerChatID: lcOwner})
	f.turn(t, 3003, 7, 1, false)

	f.lc.Shutdown(context.Background())

	inThread, inMain := callsIn(f.fake, "sendMessage")
	if len(inThread) != 1 || inThread[0].Form["chat_id"] != "3003" {
		t.Fatalf("thread sends = %v, want one attempt into chat 3003 thread 7", inThread)
	}
	var mainChats []string
	for _, c := range inMain {
		mainChats = append(mainChats, c.Form["chat_id"])
	}
	if !equalSet(mainChats, []string{"1001", "3003"}) {
		t.Fatalf("main sends to %v, want owner and 3003 (fallback)", mainChats)
	}
	if !f.b.threadGone(3003, 7) {
		t.Error("thread 7 not marked gone")
	}
}

func TestLifecycleDisabledSendsNothing(t *testing.T) {
	for _, cfg := range []LifecycleConfig{
		{Enabled: false, OwnerChatID: lcOwner}, // GOPOD_NOTIFY_LIFECYCLE=0
		{Enabled: true, OwnerChatID: 0},        // no owner configured
	} {
		f := newLCFixture(t, &fakeTelegram{}, cfg)
		f.turn(t, 2002, 0, 1, false)
		ctx := context.Background()
		f.lc.Started(ctx, 0) // first start
		f.lc.Shutdown(ctx)
		f.now = f.now.Add(time.Hour)
		f.newRun()
		f.lc.Started(ctx, 1) // graceful restart
		if got := f.sends(); len(got) != 0 {
			t.Errorf("cfg %+v: sends = %q, want none", cfg, got)
		}
		// State is still kept, so enabling later reports no phantom crash.
		if v, _ := f.st.GetState(ctx, stateStartedAt); v == "" {
			t.Errorf("cfg %+v: started marker not kept while disabled", cfg)
		}
	}
}

func TestLifecycleAcrossRestarts(t *testing.T) {
	f := newLCFixture(t, &fakeTelegram{}, LifecycleConfig{Enabled: true, OwnerChatID: lcOwner})
	ctx := context.Background()
	step := func(name string, want []string) {
		t.Helper()
		got := f.sends()
		if !equalSet(got, want) {
			t.Fatalf("%s: sends = %q, want %q", name, got, want)
		}
		f.fake.mu.Lock()
		f.fake.calls = nil
		f.fake.mu.Unlock()
	}

	f.lc.Started(ctx, 0)
	step("first start", nil)

	f.lc.Shutdown(ctx)
	step("shutdown", []string{"1001/0: " + noticeShutdown})

	f.now = f.now.Add(time.Minute)
	f.newRun()
	f.lc.Started(ctx, 2)
	step("graceful restart", []string{"1001/0: ✅ Снова на связи (версия v1). Продолжаю 2 незавершённых запроса."})
	if v, _ := f.st.GetState(ctx, stateGracefulShutdownAt); v != "" {
		t.Fatalf("graceful marker = %q after startup, want cleared", v)
	}

	// Deploy restarts gopod again quickly: no second "back".
	f.lc.Shutdown(ctx)
	f.now = f.now.Add(30 * time.Second)
	f.newRun()
	f.lc.Started(ctx, 0)
	step("quick second restart", []string{"1001/0: " + noticeShutdown})

	// Crash (no Shutdown) well after the last notice.
	f.now = f.now.Add(10 * time.Minute)
	f.newRun()
	f.lc.Started(ctx, 1)
	step("crash", []string{"1001/0: ⚠️ Был неожиданный перезапуск (версия v1). Продолжаю 1 незавершённый запрос."})

	// Crash loop: the next crash inside the window is only logged.
	f.now = f.now.Add(time.Minute)
	f.newRun()
	f.lc.Started(ctx, 0)
	step("crash loop", nil)
	f.now = f.now.Add(2 * time.Minute)
	f.newRun()
	f.lc.Started(ctx, 0)
	step("crash after window", []string{"1001/0: ⚠️ Был неожиданный перезапуск (версия v1)."})
}

func TestStartedNotifiesOwnerOnly(t *testing.T) {
	f := newLCFixture(t, &fakeTelegram{}, LifecycleConfig{Enabled: true, OwnerChatID: lcOwner})
	ctx := context.Background()
	f.lc.Started(ctx, 0)
	// Replayed turns in other chats and threads: their resumed turn
	// tells them, the startup notice must not.
	f.turn(t, 2002, 0, 1, false)
	f.turn(t, 3003, 5, 1, false)
	f.lc.Shutdown(ctx)
	f.fake.mu.Lock()
	f.fake.calls = nil
	f.fake.mu.Unlock()

	f.now = f.now.Add(time.Hour)
	f.newRun()
	f.lc.Started(ctx, 2)
	got := f.sends()
	if len(got) != 1 || !strings.HasPrefix(got[0], "1001/0: ✅") {
		t.Fatalf("startup sends = %q, want one notice to the owner's main chat", got)
	}
}

func TestSignalDuringStartupKeepsMarker(t *testing.T) {
	f := newLCFixture(t, &fakeTelegram{}, LifecycleConfig{Enabled: true, OwnerChatID: lcOwner})
	ctx := context.Background()
	f.lc.Started(ctx, 0) // run 1
	f.newRun()
	// Run 2 gets SIGTERM before its Started ran: the marker it wrote
	// must survive for run 3, not be cleared as run 1's.
	f.lc.Shutdown(ctx)
	f.lc.Started(ctx, 0)
	if v, _ := f.st.GetState(ctx, stateGracefulShutdownAt); v == "" {
		t.Fatal("graceful marker of this run cleared by its own late Started")
	}
	f.fake.mu.Lock()
	f.fake.calls = nil
	f.fake.mu.Unlock()
	f.now = f.now.Add(time.Hour)
	f.newRun()
	f.lc.Started(ctx, 0)
	if got := f.sends(); len(got) != 1 || !strings.HasPrefix(got[0], "1001/0: ✅") {
		t.Fatalf("run 3 sends = %q, want the graceful back notice", got)
	}
}

func TestNilLifecycleIsNoop(t *testing.T) {
	var l *Lifecycle
	l.Started(context.Background(), 3)
	l.Shutdown(context.Background())
}
