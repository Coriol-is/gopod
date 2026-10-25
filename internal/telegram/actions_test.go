package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// failedTurn records a finished turn of the owner chat in topic 7.
func failedTurn(t *testing.T, b *Bot, chatID int64) int64 {
	t.Helper()
	id, _, err := b.store.InsertTurn(context.Background(), store.Turn{
		Source: "telegram", SourceID: fmt.Sprintf("%d:20", chatID), ChatFolder: "owner",
		ChatID: chatID, TGMessageID: 20, ThreadID: 7, Text: "original question",
		FilePath: "/workspace/chat/a.png",
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRetryCallback_EnqueuesOriginalTextSameThread(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id := failedTurn(t, b, streamChat)

	// The button sits in topic 9 (the turn's row says 7): the retry
	// answers where the button is.
	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 9, 99, fmt.Sprintf("rt:%d", id)))

	if toast := answeredOnce(t, fake, "q1"); toast != retryToastQueued {
		t.Fatalf("toast = %q", toast)
	}
	got := mt.inserted()
	if len(got) != 1 {
		t.Fatalf("enqueued %d turns, want 1", len(got))
	}
	it := got[0]
	if it.Text != "original question" || it.ThreadID != 9 || it.ChatID != streamChat ||
		it.Folder != "owner" || !it.IsOwner || it.MessageID != 20 ||
		it.FilePath != "/workspace/chat/a.png" || it.Source != "telegram" || it.SourceID != "cb:q1" {
		t.Fatalf("retry item = %+v", it)
	}
	// The Retry button is taken off the error reply.
	m := fake.callsTo("editMessageReplyMarkup")
	if len(m) != 1 || m[0].Form["message_id"] != "99" || !cleared(t, m[0]) {
		t.Fatalf("keyboard clear = %+v", m)
	}
}

func TestRetryCallback_DoubleTapOneTurn(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id := failedTurn(t, b, streamChat)
	data := fmt.Sprintf("rt:%d", id)
	ctx := context.Background()

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 99, data))
	b.callbackHandler(ctx, b.api, cbUpdate("q2", streamChat, 7, 99, data)) // second tap
	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 99, data)) // redelivered update

	// After a restart the in-memory set is empty; a redelivered update
	// is still one turn (SourceID cb:<query id>).
	b.errMu.Lock()
	b.retried = nil
	b.errMu.Unlock()
	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 7, 99, data))

	if n := len(mt.inserted()); n != 1 {
		t.Fatalf("enqueued %d turns, want 1", n)
	}
	if toast := answeredOnce(t, fake, "q2"); toast != retryToastAlready {
		t.Fatalf("second tap toast = %q", toast)
	}
}

func TestRetryCallback_OtherChatRefused(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id := failedTurn(t, b, streamChat+3) // a turn of another chat

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 99, fmt.Sprintf("rt:%d", id)))
	b.callbackHandler(context.Background(), b.api, cbUpdate("q2", streamChat, 0, 99, "rt:999999"))

	if n := len(mt.inserted()); n != 0 {
		t.Fatalf("enqueued %d turns, want 0", n)
	}
	if toast := answeredOnce(t, fake, "q1"); toast != cbToastNotYours {
		t.Fatalf("toast = %q", toast)
	}
	if toast := answeredOnce(t, fake, "q2"); toast != cbToastExpired {
		t.Fatalf("unknown turn toast = %q", toast)
	}
}

// The login button runs the /login flow for the chat it sits in.
func TestLoginCallbackStartsLoginInThread(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 7, 99, "lg"))

	if toast := answeredOnce(t, fake, "q1"); toast != loginToastStarting {
		t.Fatalf("toast = %q", toast)
	}
	// No runner in tests: the flow answers like /login does, in topic 7.
	waitFor(t, "login reply", func() bool {
		for _, c := range fake.callsTo("sendMessage") {
			if strings.Contains(c.Form["text"], "Runner is not available") && c.Form["message_thread_id"] == "7" {
				return true
			}
		}
		return false
	})
}

// A retry whose turn could not be saved says so, keeps the button and
// can be pressed again.
func TestRetryCallback_NotSavedReportsFailure(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	mt.fail = true
	id := failedTurn(t, b, streamChat)
	data := fmt.Sprintf("rt:%d", id)

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 7, 99, data))

	if toast := answeredOnce(t, fake, "q1"); toast != retryToastFailed {
		t.Fatalf("toast = %q", toast)
	}
	if n := len(fake.callsTo("editMessageReplyMarkup")); n != 0 {
		t.Fatal("button removed although nothing was queued")
	}
	mt.mu.Lock()
	mt.fail = false
	mt.mu.Unlock()
	b.callbackHandler(context.Background(), b.api, cbUpdate("q2", streamChat, 7, 99, data))
	if toast := answeredOnce(t, fake, "q2"); toast != retryToastQueued || len(mt.inserted()) != 1 {
		t.Fatalf("second press toast = %q, queued %d", toast, len(mt.inserted()))
	}
}

func TestOnCallback_DuplicateActPanics(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	b.registerCallbacks()
	defer func() {
		if recover() == nil {
			t.Fatal("second registration of st did not panic")
		}
	}()
	b.onCallback("st", b.stopCallback)
}

// loginStub replaces the login exec: each start waits for a result on
// results (true: a session was stored) and is counted.
type loginStub struct {
	calls   atomic.Int32
	results chan bool
}

func stubLogin(b *Bot) *loginStub {
	s := &loginStub{results: make(chan bool)}
	b.loginRun = func(_ context.Context, m *models.Message, _ int, _ store.RegisteredChat) bool {
		s.calls.Add(1)
		ok := <-s.results
		if ok {
			b.logins.set(m.Chat.ID, &loginSession{chatID: m.Chat.ID})
		}
		return ok
	}
	return s
}

func TestLoginCallback_ConcurrentPressesStartOne(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	stub := stubLogin(b)
	ctx := context.Background()

	var wg sync.WaitGroup
	for _, id := range []string{"q1", "q2"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.callbackHandler(ctx, b.api, cbUpdate(id, streamChat, 7, 99, "lg"))
		}()
	}
	wg.Wait()
	waitFor(t, "login started", func() bool { return stub.calls.Load() == 1 })
	toasts := answeredOnce(t, fake, "q1") + "|" + answeredOnce(t, fake, "q2")
	if !strings.Contains(toasts, loginToastStarting) || !strings.Contains(toasts, loginToastAlready) {
		t.Fatalf("toasts = %q, want one start and one refusal", toasts)
	}
	// The press takes the 🔑 button off the reply.
	if m := fake.callsTo("editMessageReplyMarkup"); len(m) != 1 || !cleared(t, m[0]) {
		t.Fatalf("keyboard clears = %+v", m)
	}
	// /login while it starts is refused too.
	b.startLogin(ctx, &models.Message{ID: 5, Chat: models.Chat{ID: streamChat}}, 0)
	if stub.calls.Load() != 1 {
		t.Fatal("/login started a second login")
	}

	// The start fails: the slot is released and a new press starts again.
	stub.results <- false
	waitFor(t, "slot released", func() bool { return b.logins.reserve(streamChat) })
	b.logins.release(streamChat)
	b.callbackHandler(ctx, b.api, cbUpdate("q3", streamChat, 7, 99, "lg"))
	waitFor(t, "second start", func() bool { return stub.calls.Load() == 2 })
	stub.results <- false
}

func TestLoginCallback_AfterSessionFinishedStartsNew(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	stub := stubLogin(b)
	ctx := context.Background()

	b.callbackHandler(ctx, b.api, cbUpdate("q1", streamChat, 0, 99, "lg"))
	stub.results <- true // session stored
	waitFor(t, "session", func() bool { return b.logins.get(streamChat) != nil })

	b.callbackHandler(ctx, b.api, cbUpdate("q2", streamChat, 0, 99, "lg"))
	if toast := answeredOnce(t, fake, "q2"); toast != loginToastAlready {
		t.Fatalf("press during a session: toast %q", toast)
	}

	b.logins.remove(streamChat) // finished or timed out
	b.callbackHandler(ctx, b.api, cbUpdate("q3", streamChat, 0, 99, "lg"))
	waitFor(t, "new login", func() bool { return stub.calls.Load() == 2 })
	stub.results <- false
}

func TestLoginFailed_NoErrorText(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)

	b.loginFailed(context.Background(), streamChat, 7, "owner", "ensure container",
		errors.New("docker: create /srv/gopod/data/owner: token=sk-SECRET permission denied"))

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["text"] != loginTextFailed || sends[0].Form["message_thread_id"] != "7" {
		t.Fatalf("sends = %+v", sends)
	}
}
