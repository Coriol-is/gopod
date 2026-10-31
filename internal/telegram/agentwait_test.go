package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

func okResponder(method string, _ map[string]string) (int, string) {
	if method == "sendMessage" || method == "editMessageText" {
		return sentMessage("88")
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
func TestWaitNoticeDraftIsStoppableAndReplaced(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	ctx := context.Background()
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer b.registerInflight(item.ID, item.ChatID, 0, cancel)()
	defer b.stopWaitDraftRefresh(item)

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

	// Once the agent runs, streamReply's opening draft replaces it.
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	close(chunks)
	result <- runner.RunResult{FullText: "done"}
	b.streamReply(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result})
	drafts = fake.callsTo("sendMessageDraft")
	if len(drafts) < 2 || drafts[1].Form["draft_id"] == drafts[0].Form["draft_id"] ||
		strings.Contains(drafts[1].Form["text"], "⏳") {
		t.Fatalf("opening draft did not replace the wait draft: %+v", drafts)
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
	if b.popWaitPlaceholder(item) != 0 {
		t.Fatal("wait placeholder left behind")
	}
}

func TestWaitNoticeResumedTurnEditsItsPlaceholder(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	item.Resumed, item.PlaceholderMsgID = true, 42
	b.waitNotice(context.Background(), item)(runner.WaitSlot)
	edits := fake.callsTo("editMessageText")
	if len(edits) != 1 || edits[0].Form["message_id"] != "42" || edits[0].Form["text"] != waitSlotText {
		t.Fatalf("edits = %+v", edits)
	}
	if n := len(fake.callsTo("sendMessage")) + len(fake.callsTo("sendMessageDraft")); n != 0 {
		t.Fatalf("extra sends: %d", n)
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
			if turn.ReplyMsgID != 88 {
				t.Fatalf("stopped reply not recorded: %d", turn.ReplyMsgID)
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

	b.stopWaitDraftRefresh(item)
	n := len(waitDrafts(fake))
	time.Sleep(50 * time.Millisecond)
	if got := len(waitDrafts(fake)); got != n {
		t.Fatalf("wait draft sent %d more times after the refresher stopped", got-n)
	}
	b.stopWaitDraftRefresh(item) // idempotent, no refresher left
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
	defer b.stopWaitDraftRefresh(item)

	b.waitNotice(context.Background(), item)(runner.WaitSlot)
	waitFor(t, "rate-limited refresh", func() bool { return drafts.Load() >= 2 })
	time.Sleep(300 * time.Millisecond) // well inside retry_after=1s
	if n := drafts.Load(); n != 2 {
		t.Fatalf("draft sends = %d during retry_after, want 2", n)
	}
	waitFor(t, "refresh after retry_after", func() bool { return drafts.Load() >= 3 })
}

// Shutdown (the turn's ctx ends) stops the refresher by itself; the
// later stopWaitDraftRefresh returns at once.
func TestWaitDraftRefreshEndsWithContext(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.waitDraftEvery = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	b.waitNotice(ctx, item)(runner.WaitSlot)
	waitFor(t, "a refresh", func() bool { return len(waitDrafts(fake)) >= 2 })
	b.waitMu.Lock()
	rf := b.waitRefresh[waitKeyOf(item)]
	b.waitMu.Unlock()
	cancel()
	select {
	case <-rf.done: // exited on its own, before anyone stopped it
	case <-time.After(time.Second):
		t.Fatal("refresher did not exit after ctx was cancelled")
	}
	b.stopWaitDraftRefresh(item) // returns at once
	n := len(waitDrafts(fake))
	time.Sleep(40 * time.Millisecond)
	if got := len(waitDrafts(fake)); got != n {
		t.Fatalf("refresher kept sending after cancel")
	}
}
