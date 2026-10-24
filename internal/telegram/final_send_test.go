package telegram

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// tooManyRequests renders a Bot API 429 with retry_after seconds.
func tooManyRequests(retryAfter string) (int, string) {
	return http.StatusTooManyRequests, `{"ok":false,"error_code":429,` +
		`"description":"Too Many Requests: retry after ` + retryAfter + `",` +
		`"parameters":{"retry_after":` + retryAfter + `}}`
}

// recordSleeps replaces the bot's wait with one that records the
// requested durations and returns at once.
func recordSleeps(b *Bot) func() []time.Duration {
	var mu sync.Mutex
	var waits []time.Duration
	b.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		waits = append(waits, d)
		return nil
	}
	return func() []time.Duration {
		mu.Lock()
		defer mu.Unlock()
		return append([]time.Duration(nil), waits...)
	}
}

// runStreamToEnd streams text through streamReply and returns its error.
func runStreamToEnd(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, text string) error {
	t.Helper()
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	chunks <- text
	waitFor(t, "draft "+text, draftShowing(fake, text))
	close(chunks)
	result <- runner.RunResult{FullText: text}
	close(result)
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("streamReply did not return")
		return nil
	}
}

func TestFinalSend_RetriesAfter429ThenRecordsOnce(t *testing.T) {
	var sends int32
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			if atomic.AddInt32(&sends, 1) == 1 {
				return tooManyRequests("2")
			}
			return sentMessage("888")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	waits := recordSleeps(b)

	if err := runStreamToEnd(t, b, fake, item, "final answer"); err != nil {
		t.Fatalf("streamReply err = %v, want nil", err)
	}
	if got := atomic.LoadInt32(&sends); got != 2 {
		t.Errorf("sendMessage calls = %d, want 2 (429, then one success)", got)
	}
	if w := waits(); len(w) != 1 || w[0] != 2*time.Second {
		t.Errorf("waits = %v, want [2s] from retry_after", w)
	}
	row, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ReplyMsgID != 888 {
		t.Errorf("reply_msg_id = %d, want 888", row.ReplyMsgID)
	}
	if got := countBotMessages(t, b, item); got != 1 {
		t.Errorf("recorded replies = %d, want 1", got)
	}
}

// TestFinalSend_PermanentFailureIsNotSilentDone: when the final message
// cannot be delivered, streamReply returns errReplyUndelivered (the
// queue then retries the turn and, past its retry cap, marks it
// failed) instead of nil, which would mark it done.
func TestFinalSend_PermanentFailureIsNotSilentDone(t *testing.T) {
	cases := map[string]func() (int, string){
		"429 every time": func() (int, string) { return tooManyRequests("5") },
		"server error":   func() (int, string) { return apiError(http.StatusInternalServerError, "Internal Server Error") },
	}
	for name, fail := range cases {
		t.Run(name, func(t *testing.T) {
			fake := &fakeTelegram{}
			fake.respond = func(method string, _ map[string]string) (int, string) {
				if method == "sendMessage" {
					return fail()
				}
				return http.StatusOK, `{"ok":true,"result":true}`
			}
			b, item := newStreamFixture(t, fake)
			waits := recordSleeps(b)

			err := runStreamToEnd(t, b, fake, item, "lost answer")
			if !errors.Is(err, errReplyUndelivered) {
				t.Fatalf("streamReply err = %v, want errReplyUndelivered", err)
			}
			row, _ := b.store.GetTurn(context.Background(), item.ID)
			if row.ReplyMsgID != 0 {
				t.Errorf("reply_msg_id = %d, want 0", row.ReplyMsgID)
			}
			var total time.Duration
			for _, w := range waits() {
				total += w
			}
			if total > finalSendMaxWait {
				t.Errorf("waited %v in total, cap is %v", total, finalSendMaxWait)
			}
			if n := len(fake.callsTo("sendMessage")); n > 2*finalSendAttempts {
				t.Errorf("sendMessage calls = %d, want at most %d", n, 2*finalSendAttempts)
			}
		})
	}
}

func TestDraftTicker_BacksOffAfter429(t *testing.T) {
	var drafts int32
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessage":
			return sentMessage("999")
		case "sendMessageDraft":
			if atomic.AddInt32(&drafts, 1) >= 2 { // opening draft ok, then 429
				return tooManyRequests("30")
			}
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake) // 10ms tick
	recordSleeps(b)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	chunks <- "a"
	waitFor(t, "a draft hitting 429", func() bool { return atomic.LoadInt32(&drafts) >= 2 })
	for i := 0; i < 20; i++ { // ~20 ticks of new text inside the retry_after window
		chunks <- "b"
		time.Sleep(10 * time.Millisecond)
	}
	close(chunks)
	result <- runner.RunResult{FullText: "ab"}
	close(result)
	if err := <-errc; err != nil {
		t.Fatalf("streamReply err = %v", err)
	}
	if got := atomic.LoadInt32(&drafts); got != 2 {
		t.Errorf("draft calls = %d, want 2 (no draft until retry_after passes)", got)
	}
}

func TestDraftIDsDoNotRestartAtOne(t *testing.T) {
	a := &Bot{}
	first := a.newDraftID()
	if first == 1 {
		t.Error("first draft id is 1: a stale Stop from before a restart could match it")
	}
	if first <= 0 || first > 1<<31-1 {
		t.Errorf("draft id %d outside (0, MaxInt32]", first)
	}
	if next := a.newDraftID(); next != first+1 {
		t.Errorf("second id = %d, want %d", next, first+1)
	}
	time.Sleep(3 * time.Millisecond)
	if other := (&Bot{}).newDraftID(); other == first {
		t.Errorf("two bots started at different times share first id %d", other)
	}
}
