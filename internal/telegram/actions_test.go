package telegram

import (
	"context"
	"errors"
	"fmt"
	"html"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
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
	if !strings.Contains(toasts, loginToastStarting) || !strings.Contains(toasts, loginToastInStart) {
		t.Fatalf("toasts = %q, want one start and one refusal", toasts)
	}
	// The press takes the 🔑 button off the reply.
	if m := fake.callsTo("editMessageReplyMarkup"); len(m) != 1 || !cleared(t, m[0]) {
		t.Fatalf("keyboard clears = %+v", m)
	}
	// /login while it starts is refused too, saying it is starting.
	b.startLogin(ctx, &models.Message{ID: 5, Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}}, 0)
	if stub.calls.Load() != 1 {
		t.Fatal("/login started a second login")
	}
	waitFor(t, "starting notice", func() bool {
		for _, c := range fake.callsTo("sendMessage") {
			if c.Form["text"] == loginTextStarting {
				return true
			}
		}
		return false
	})

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

// A panic while starting a login must not keep the chat's slot.
func TestLoginReserved_PanicReleasesSlot(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	b.loginRun = func(context.Context, *models.Message, int, store.RegisteredChat) bool {
		panic("exec exploded")
	}
	if !b.logins.reserve(streamChat) {
		t.Fatal("reserve")
	}
	func() {
		defer func() { _ = recover() }()
		b.loginReserved(context.Background(), &models.Message{Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}}, 0, store.RegisteredChat{Folder: "owner"})
	}()

	stub := stubLogin(b)
	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 99, "lg"))
	if toast := answeredOnce(t, fake, "q1"); toast != loginToastStarting {
		t.Fatalf("press after a panicked start: toast %q", toast)
	}
	waitFor(t, "login started", func() bool { return stub.calls.Load() == 1 })
	stub.results <- false
}

// pressed runs one button press and returns its toast.
func pressed(t *testing.T, b *Bot, fake *fakeTelegram, qid string, thread, msgID int, data string) string {
	t.Helper()
	b.callbackHandler(context.Background(), b.api, cbUpdate(qid, streamChat, thread, msgID, data))
	return answeredOnce(t, fake, qid)
}

func TestRegenerate_SameThreadNewTurn(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id := failedTurn(t, b, streamChat) // topic 7, "original question"
	data := fmt.Sprintf("rg:%d", id)

	if toast := pressed(t, b, fake, "q1", 7, 99, data); toast != regenToastQueued {
		t.Fatalf("toast = %q", toast)
	}
	if toast := pressed(t, b, fake, "q2", 7, 99, data); toast != regenToastAlready {
		t.Fatalf("second tap toast = %q", toast)
	}
	got := mt.inserted()
	if len(got) != 1 {
		t.Fatalf("enqueued %d turns, want 1", len(got))
	}
	it := got[0]
	if it.ThreadID != 7 || it.ChatID != streamChat || it.Folder != "owner" || !it.IsOwner ||
		it.MessageID != 20 || it.FilePath != "/workspace/chat/a.png" ||
		it.Source != "telegram" || it.SourceID != "cb:q1" ||
		it.Text != regeneratePrefix+"original question" {
		t.Fatalf("regenerate item = %+v", it)
	}
	// The pressed row goes at once; the new reply brings its own.
	m := fake.callsTo("editMessageReplyMarkup")
	if len(m) != 1 || m[0].Form["message_id"] != "99" || !cleared(t, m[0]) {
		t.Fatalf("keyboard clear = %+v", m)
	}
}

func TestRegenerate_TurnFromOtherChatRefused(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id := failedTurn(t, b, streamChat+3) // a turn of another chat

	for i, data := range []string{
		fmt.Sprintf("rg:%d", id), fmt.Sprintf("mo:%d", id), fmt.Sprintf("mb:%d", id),
		fmt.Sprintf("st2:%d", id), fmt.Sprintf("af:%d", id),
	} {
		if toast := pressed(t, b, fake, fmt.Sprint("q", i), 0, 99, data); toast != cbToastNotYours {
			t.Errorf("%s toast = %q", data, toast)
		}
	}
	if toast := pressed(t, b, fake, "qx", 0, 99, "rg:999999"); toast != cbToastExpired {
		t.Errorf("unknown turn toast = %q", toast)
	}
	if n := len(mt.inserted()); n != 0 {
		t.Fatalf("enqueued %d turns, want 0", n)
	}
	for _, m := range []string{"editMessageReplyMarkup", "sendMessage", "sendDocument"} {
		if n := len(fake.callsTo(m)); n != 0 {
			t.Errorf("%s called %d times for another chat's turn", m, n)
		}
	}
}

// Nothing in memory names the turn after a restart: Regenerate reads it
// from the store. Regenerating a regenerated turn keeps one prefix.
func TestRegenerate_WorksAfterRestart(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id, _, err := b.store.InsertTurn(context.Background(), store.Turn{
		Source: "telegram", SourceID: "cb:old", ChatFolder: "owner", ChatID: streamChat,
		TGMessageID: 30, Text: regeneratePrefix + "вопрос",
	})
	if err != nil {
		t.Fatal(err)
	}
	if toast := pressed(t, b, fake, "q1", 0, 99, fmt.Sprintf("rg:%d", id)); toast != regenToastQueued {
		t.Fatalf("toast = %q", toast)
	}
	got := mt.inserted()
	if len(got) != 1 || got[0].Text != regeneratePrefix+"вопрос" || got[0].ThreadID != 0 {
		t.Fatalf("items = %+v", got)
	}
}

// stepsTurn streams a turn of the owner chat in topic 7 with tool steps
// and returns it.
func stepsTurn(t *testing.T, b *Bot, evs ...runner.ProgressEvent) queue.Item {
	t.Helper()
	item := turnIn(t, b, 7, 21)
	item.IsOwner = true
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()
	for _, ev := range evs {
		s.progress <- ev
	}
	s.finish("итоговый ответ")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	return item
}

func TestSteps_SentAsExpandableQuoteRedacted(t *testing.T) {
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	secret := runner.ProgressEvent{Tool: "Bash", Detail: "curl -H 'Authorization: Bearer abcdef1234567890' https://x"}
	item := stepsTurn(t, b,
		runner.ProgressEvent{Tool: "Read", Detail: "/workspace/chat/main.go"},
		secret)

	if toast := pressed(t, b, fake, "q1", 7, 500, fmt.Sprintf("st2:%d", item.ID)); toast != "" {
		t.Fatalf("toast = %q", toast)
	}
	sends := fake.callsTo("sendMessage")
	c := sends[len(sends)-1]
	text := c.Form["text"]
	if !strings.HasPrefix(text, "<blockquote expandable>") || !strings.Contains(text, "main.go") ||
		!strings.Contains(text, html.EscapeString(progressLine(secret))) || strings.Contains(text, "abcdef1234567890") {
		t.Fatalf("steps message = %q", text)
	}
	if c.Form["message_thread_id"] != "7" || !strings.Contains(c.Form["reply_parameters"], `"message_id":500`) {
		t.Fatalf("steps message routing = %v", c.Form)
	}
}

func TestSteps_ExpiredToast(t *testing.T) {
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	now := time.Now()
	fakeClock(b, &now)
	item := stepsTurn(t, b, runner.ProgressEvent{Tool: "Grep", Detail: "TODO"})
	data := fmt.Sprintf("st2:%d", item.ID)

	now = now.Add(memoTTL + time.Second)
	if toast := pressed(t, b, fake, "q1", 7, 500, data); toast != stepsToastNone {
		t.Fatalf("expired toast = %q", toast)
	}
	if toast := pressed(t, b, fake, "q2", 7, 500, fmt.Sprintf("af:%d", item.ID)); toast != fileToastGone {
		t.Fatalf("expired as-file toast = %q", toast)
	}
	// A plain run kept no steps at all.
	plain := turnIn(t, b, 0, 22)
	if toast := pressed(t, b, fake, "q3", 0, 501, fmt.Sprintf("st2:%d", plain.ID)); toast != stepsToastNone {
		t.Fatalf("no-steps toast = %q", toast)
	}
	if n := len(fake.callsTo("sendDocument")); n != 0 {
		t.Fatalf("documents sent = %d", n)
	}
}

func TestAsFile_SendsDocumentInThread(t *testing.T) {
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	item := turnIn(t, b, 7, 21)
	reply := "# Ответ\n\n" + strings.Repeat("строка\n", 50)
	if err := streamAll(t, b, item, reply); err != nil {
		t.Fatal(err)
	}
	if toast := pressed(t, b, fake, "q1", 7, 500, fmt.Sprintf("af:%d", item.ID)); toast != "" {
		t.Fatalf("toast = %q", toast)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("documents = %d, want 1", len(docs))
	}
	d := docs[0]
	if d.Form["document.filename"] != "reply.md" || d.Form["document.content"] != reply ||
		d.Form["message_thread_id"] != "7" {
		t.Fatalf("document = %v", d.Form)
	}
}

func TestMoreBack_SwapsMarkupOnly(t *testing.T) {
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	item := turnIn(t, b, 0, 21)
	if err := streamAll(t, b, item, "короткий ответ"); err != nil {
		t.Fatal(err)
	}
	texts := len(fake.callsTo("editMessageText")) + len(fake.callsTo("sendMessage"))
	id := strconv.FormatInt(item.ID, 10)

	if toast := pressed(t, b, fake, "q1", 0, 500, "mo:"+id); toast != "" {
		t.Fatalf("more toast = %q", toast)
	}
	if toast := pressed(t, b, fake, "q2", 0, 500, "mb:"+id); toast != "" {
		t.Fatalf("back toast = %q", toast)
	}
	m := fake.callsTo("editMessageReplyMarkup")
	if len(m) != 2 || m[0].Form["message_id"] != "500" || m[1].Form["message_id"] != "500" {
		t.Fatalf("markup edits = %+v", m)
	}
	more, _ := markupData(t, m[0])
	if strings.Join(more, ",") != "st2:"+id+",af:"+id+",mb:"+id {
		t.Fatalf("more row = %v", more)
	}
	if !hasAct(t, m[1], "rg") || !hasAct(t, m[1], "mo") ||
		!strings.Contains(m[1].Form["reply_markup"], `"copy_text":{"text":"короткий ответ"}`) {
		t.Fatalf("back row = %s", m[1].Form["reply_markup"])
	}
	if n := len(fake.callsTo("editMessageText")) + len(fake.callsTo("sendMessage")); n != texts {
		t.Fatalf("more/back sent or edited text (%d -> %d calls)", texts, n)
	}
}

// A scheduled task's turn is not a chat turn: no reply button acts on it,
// even when its row names the chat.
func TestReplyActions_TaskTurnRefused(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	id, _, err := b.store.InsertTurn(context.Background(), store.Turn{
		Source: "task", SourceID: "task-1:123", ChatFolder: "owner", ChatID: streamChat, Text: "daily report",
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, act := range []string{"rg", "mo", "mb", "st2", "af"} {
		if toast := pressed(t, b, fake, fmt.Sprint("q", i), 0, 99, fmt.Sprintf("%s:%d", act, id)); toast != cbToastNotYours {
			t.Errorf("%s toast = %q", act, toast)
		}
	}
	if n := len(mt.inserted()); n != 0 {
		t.Fatalf("enqueued %d turns, want 0", n)
	}
}

// A regenerate whose turn could not be saved says so, keeps the row and
// can be pressed again.
func TestRegenerate_NotSavedReportsFailure(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	mt := withQueue(t, b)
	mt.fail = true
	id := failedTurn(t, b, streamChat)
	data := fmt.Sprintf("rg:%d", id)

	if toast := pressed(t, b, fake, "q1", 7, 99, data); toast != regenToastFailed {
		t.Fatalf("toast = %q", toast)
	}
	if n := len(fake.callsTo("editMessageReplyMarkup")); n != 0 {
		t.Fatalf("row cleared %d times after a failed regenerate", n)
	}
	mt.mu.Lock()
	mt.fail = false
	mt.mu.Unlock()
	if toast := pressed(t, b, fake, "q2", 7, 99, data); toast != regenToastQueued {
		t.Fatalf("second press toast = %q", toast)
	}
	if n := len(mt.inserted()); n != 1 {
		t.Fatalf("enqueued %d turns, want 1", n)
	}
}
