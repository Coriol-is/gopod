package telegram

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/go-telegram/bot"
)

func okResponder(method string, form map[string]string) (int, string) {
	if method == "sendMessage" {
		return sentMessage("88")
	}
	if method == "editMessageText" || method == "editMessageReplyMarkup" {
		return sentMessage(form["message_id"])
	}
	return http.StatusOK, `{"ok":true,"result":true}`
}

func TestWaitTexts(t *testing.T) {
	if got := waitText(runner.WaitSlot); got != "⏳ ждёт свободный слот агента…" {
		t.Errorf("slot text = %q", got)
	}
	if got := waitText(runner.WaitMemory); got != "⏳ ждёт свободную память…" {
		t.Errorf("memory text = %q", got)
	}
}

// Draft path: the wait shows as a stoppable draft bound to the turn, and
// the opening draft of streamReply (a new draft id) replaces it.
func TestWaitNoticeDraftStopSealsOpeningPreview(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	ctx := context.Background()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.registerInflight(item.ID, item.ChatID, 0, cancel)()
	defer b.stopWaitDraftRefresh(t.Context(), item)

	b.waitNotice(ctx, item)(runner.WaitMemory)

	drafts := fake.callsTo("sendMessageDraft")
	if len(drafts) != 1 {
		t.Fatalf("drafts = %d, want 1", len(drafts))
	}
	if drafts[0].Form["text"] != waitMemoryText || drafts[0].Form["can_stop"] != "true" {
		t.Fatalf("wait draft = %v", drafts[0].Form)
	}
	if n := len(fake.callsTo("sendMessage")); n != 0 {
		t.Fatalf("sendMessage calls = %d on the draft path", n)
	}

	// Stop on the waiting draft cancels the turn.
	waitID, _ := strconv.Atoi(drafts[0].Form["draft_id"])
	b.handleGenerationStopped(ctx, stopUpdate(streamChat, waitID))
	if runCtx.Err() == nil {
		t.Fatal("stop on the wait draft did not cancel the turn")
	}

	// A late stream return cannot open another preview after accepted Stop.
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	close(chunks)
	result <- runner.RunResult{FullText: "done"}
	b.streamReply(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result})
	drafts = fake.callsTo("sendMessageDraft")
	if len(drafts) != 1 {
		t.Fatalf("new draft after accepted Stop: %+v", drafts)
	}
}

// Edit path: the placeholder goes out early with the wait text, is
// recorded, and streamReply takes it over instead of sending another.
func TestWaitNoticeEditPathPlaceholderTakenOver(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(streamChat)
	ctx := context.Background()

	b.waitNotice(ctx, item)(runner.WaitSlot)

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["text"] != waitSlotText {
		t.Fatalf("wait placeholder sends = %+v", sends)
	}
	turn, _ := b.store.GetTurn(ctx, item.ID)
	if turn.PlaceholderMsgID != 88 {
		t.Fatalf("placeholder not recorded: %d", turn.PlaceholderMsgID)
	}

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	close(chunks)
	result <- runner.RunResult{FullText: "**hi**"}
	b.streamReply(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result})

	if n := len(fake.callsTo("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (the early placeholder only)", n)
	}
	edits := fake.callsTo("editMessageText")
	if len(edits) < 2 || strings.Contains(edits[0].Form["text"], "⏳") {
		t.Fatalf("placeholder not turned back into the cursor first: %+v", edits)
	}
	if last := edits[len(edits)-1]; !strings.Contains(last.Form["text"], "<b>hi</b>") || last.Form["message_id"] != "88" {
		t.Fatalf("final edit = %v", last.Form)
	}
	if b.popWaitPlaceholder(t.Context(), item) != 0 {
		t.Fatal("wait placeholder left behind")
	}
}

func TestWaitNoticeDoesNotReuseOldAttemptPlaceholder(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	item.Resumed, item.PlaceholderMsgID = true, 42
	b.waitNotice(context.Background(), item)(runner.WaitSlot)
	for _, c := range fake.callsTo("editMessageText") {
		if c.Form["message_id"] == "42" {
			t.Fatal("old attempt message overwritten")
		}
	}
}

func TestStreamNotStartedStoppedWhileWaiting(t *testing.T) {
	for _, early := range []bool{false, true} {
		t.Run(map[bool]string{false: "draft", true: "placeholder"}[early], func(t *testing.T) {
			fake := &fakeTelegram{respond: okResponder}
			b, item := newStreamFixture(t, fake)
			ctx := context.Background()
			if early {
				b.markDraftsUnsupported(streamChat)
				b.waitNotice(ctx, item)(runner.WaitSlot)
			}
			runCtx, cancel := context.WithCancel(ctx)
			cancel() // the user pressed Stop while the turn waited

			handled, err := b.streamNotStarted(ctx, runCtx, item)
			if !handled || !errors.Is(err, queue.ErrStopped) {
				t.Fatalf("got (%v, %v), want (true, ErrStopped)", handled, err)
			}
			turn, _ := b.store.GetTurn(ctx, item.ID)
			if turn.ReplyMsgID != 0 {
				t.Fatalf("Stop acknowledgement recorded as final: %d", turn.ReplyMsgID)
			}
			sends := fake.callsTo("sendMessage")
			edits := fake.callsTo("editMessageText")
			if early {
				if len(sends) != 1 || len(edits) != 1 || edits[0].Form["text"] != stoppedEmpty {
					t.Fatalf("sends=%+v edits=%+v", sends, edits)
				}
			} else if len(sends) != 1 || !strings.Contains(sends[0].Form["text"], stoppedEmpty) {
				t.Fatalf("sends = %+v", sends)
			}
		})
	}
}

func TestStreamNotStartedShutdownAndLiveRun(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)

	live := context.Background()
	if handled, err := b.streamNotStarted(live, live, item); handled || err != nil {
		t.Fatalf("live run: (%v, %v), want (false, nil)", handled, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // shutdown cancels the turn context and its run context
	if handled, err := b.streamNotStarted(ctx, ctx, item); !handled || err != nil {
		t.Fatalf("shutdown: (%v, %v), want (true, nil)", handled, err)
	}
	if n := len(fake.calls); n != 0 {
		t.Fatalf("shutdown sent %d API calls", n)
	}
}

// waitDrafts returns the wait-text drafts sent so far.
func waitDrafts(fake *fakeTelegram) []apiCall {
	var out []apiCall
	for _, c := range fake.callsTo("sendMessageDraft") {
		if strings.HasPrefix(c.Form["text"], "⏳") {
			out = append(out, c)
		}
	}
	return out
}

// A wait longer than the refresh interval keeps the same stoppable
// draft alive, Stop still works on it, and after stopWaitDraftRefresh
// (what runAgentStreaming does when RunStream returns) nothing more is
// sent, so the stream's opening draft is never overwritten.
func TestWaitDraftRefreshedUntilStopped(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.waitDraftEvery = 10 * time.Millisecond
	ctx := context.Background()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.registerInflight(item.ID, item.ChatID, 0, cancel)()

	b.waitNotice(ctx, item)(runner.WaitSlot)
	waitFor(t, "refreshed wait draft", func() bool { return len(waitDrafts(fake)) >= 3 })
	drafts := waitDrafts(fake)
	for _, d := range drafts {
		if d.Form["draft_id"] != drafts[0].Form["draft_id"] || d.Form["text"] != waitSlotText || d.Form["can_stop"] != "true" {
			t.Fatalf("refresh changed the draft: %v vs %v", d.Form, drafts[0].Form)
		}
	}

	id, _ := strconv.Atoi(drafts[0].Form["draft_id"])
	b.handleGenerationStopped(ctx, stopUpdate(streamChat, id))
	if runCtx.Err() == nil {
		t.Fatal("stop on a refreshed wait draft did not cancel the turn")
	}

	b.stopWaitDraftRefresh(t.Context(), item)
	n := len(waitDrafts(fake))
	time.Sleep(50 * time.Millisecond)
	if got := len(waitDrafts(fake)); got != n {
		t.Fatalf("wait draft sent %d more times after the refresher stopped", got-n)
	}
	b.stopWaitDraftRefresh(t.Context(), item) // idempotent, no refresher left
}

func TestWaitDraftRefreshHonoursRetryAfter(t *testing.T) {
	fake := &fakeTelegram{}
	var drafts atomic.Int32
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessageDraft" && drafts.Add(1) == 2 {
			return tooManyRequests("1")
		}
		return okResponder(method, nil)
	}
	b, item := newStreamFixture(t, fake)
	b.waitDraftEvery = 10 * time.Millisecond
	defer b.stopWaitDraftRefresh(t.Context(), item)

	b.waitNotice(context.Background(), item)(runner.WaitSlot)
	waitFor(t, "rate-limited refresh", func() bool { return drafts.Load() >= 2 })
	time.Sleep(300 * time.Millisecond) // well inside retry_after=1s
	if n := drafts.Load(); n != 2 {
		t.Fatalf("draft sends = %d during retry_after, want 2", n)
	}
	waitFor(t, "refresh after retry_after", func() bool { return drafts.Load() >= 3 })
}

// waitHTTPClientFunc measures client dispatch, not when a canceled request is
// eventually recorded by the remote HTTP handler.
type waitHTTPClientFunc func(*http.Request) (*http.Response, error)

func (f waitHTTPClientFunc) Do(r *http.Request) (*http.Response, error) { return f(r) }

// Shutdown exits the refresher by itself, including while HTTP is in flight.
// Cancellation cannot retract a request already received by the remote server.
func TestWaitDraftRefreshEndsWithContext(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.waitDraftEvery = 10 * time.Millisecond
	arrived, release, recorded := make(chan struct{}), make(chan struct{}), make(chan struct{})
	var requests, dispatched atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 2 {
			// Consume the in-flight request but delay the fake's observation until
			// after the client notices cancellation and the refresher exits.
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Error(err)
			}
			close(arrived)
			<-release
			fake.ServeHTTP(w, r)
			close(recorded)
			return
		}
		fake.ServeHTTP(w, r)
	}))
	defer srv.Close()
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	client := waitHTTPClientFunc(func(r *http.Request) (*http.Response, error) {
		dispatched.Add(1)
		return srv.Client().Do(r)
	})
	var err error
	b.api, err = bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe(), bot.WithHTTPClient(time.Second, client))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	b.waitNotice(ctx, item)(runner.WaitSlot)
	select {
	case <-arrived:
	case <-time.After(time.Second):
		t.Fatal("refresh did not reach server")
	}
	b.waitMu.Lock()
	rf := b.waitRefresh[b.waitKeyOf(t.Context(), item)]
	b.waitMu.Unlock()
	// Leave another tick ready while the second HTTP call is blocked.
	time.Sleep(3 * b.waitDraftEvery)
	cancel()
	select {
	case <-rf.done:
	case <-time.After(time.Second):
		t.Fatal("refresher did not exit after ctx was cancelled")
	}
	b.stopWaitDraftRefresh(t.Context(), item)
	if got := dispatched.Load(); got != 2 {
		t.Fatalf("client dispatched %d requests, want initial + one refresh", got)
	}
	before := len(waitDrafts(fake))
	close(release)
	select {
	case <-recorded:
	case <-time.After(time.Second):
		t.Fatal("in-flight server request did not finish")
	}
	if got := len(waitDrafts(fake)); got != before+1 {
		t.Fatalf("late pre-cancel observation = %d, want %d", got, before+1)
	}
	time.Sleep(4 * b.waitDraftEvery)
	if got := dispatched.Load(); got != 2 {
		t.Fatalf("refresher dispatched after cancellation: %d", got)
	}
}
