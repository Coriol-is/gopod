package telegram

import (
	"context"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// cbUpdate is a callback_query update: button data pressed by user 42
// on message msgID in chatID (a private chat; thread is its topic).
func cbUpdate(id string, chatID int64, thread, msgID int, data string) *models.Update {
	return &models.Update{CallbackQuery: &models.CallbackQuery{
		ID:   id,
		From: models.User{ID: 42},
		Data: data,
		Message: models.MaybeInaccessibleMessage{
			Type: models.MaybeInaccessibleMessageTypeMessage,
			Message: &models.Message{
				ID:              msgID,
				Chat:            models.Chat{ID: chatID, Type: models.ChatTypePrivate},
				MessageThreadID: thread,
			},
		},
	}}
}

// newCallbackFixture is a stream fixture whose chat is the owner chat,
// so resolveRegistered registers it on first sight.
func newCallbackFixture(t *testing.T, fake *fakeTelegram) *Bot {
	t.Helper()
	b, _ := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	return b
}

// answers returns the answerCallbackQuery calls for query id.
func answers(fake *fakeTelegram, id string) []apiCall {
	var out []apiCall
	for _, c := range fake.callsTo("answerCallbackQuery") {
		if c.Form["callback_query_id"] == id {
			out = append(out, c)
		}
	}
	return out
}

// answeredOnce asserts exactly one answer for id and returns its toast.
func answeredOnce(t *testing.T, fake *fakeTelegram, id string) string {
	t.Helper()
	a := answers(fake, id)
	if len(a) != 1 {
		t.Fatalf("answerCallbackQuery for %s called %d times, want 1", id, len(a))
	}
	return a[0].Form["text"]
}

func TestEncodeCB_RejectsOver64Bytes(t *testing.T) {
	if _, err := encodeCB("rt", strings.Repeat("9", 70)); err == nil {
		t.Error("70-byte arg accepted")
	}
	if s, err := encodeCB("rt", strings.Repeat("9", 61)); err != nil || len(s) != 64 {
		t.Errorf("64-byte data = %q, %v", s, err)
	}
	if _, err := encodeCB("rt", strings.Repeat("9", 62)); err == nil {
		t.Error("65-byte data accepted")
	}
	if _, err := encodeCB("rt", "1:2"); err == nil {
		t.Error("arg with ':' accepted")
	}
	if _, err := encodeCB(""); err == nil {
		t.Error("empty act accepted")
	}
	if _, err := encodeCB("a:b"); err == nil {
		t.Error("act with ':' accepted")
	}
}

func TestDecodeCB_RoundTrip(t *testing.T) {
	s, err := encodeCB("st", "123", "x")
	if err != nil || s != "st:123:x" {
		t.Fatalf("encodeCB = %q, %v", s, err)
	}
	d, ok := decodeCB(s)
	if !ok || d.Act != "st" || len(d.Args) != 2 || d.Args[0] != "123" || d.Args[1] != "x" {
		t.Fatalf("decodeCB(%q) = %+v, %v", s, d, ok)
	}
	if d, ok := decodeCB("lg"); !ok || d.Act != "lg" || len(d.Args) != 0 {
		t.Fatalf("decodeCB(lg) = %+v, %v", d, ok)
	}
	for _, bad := range []string{"", ":1", strings.Repeat("a", 65)} {
		if _, ok := decodeCB(bad); ok {
			t.Errorf("decodeCB(%q) accepted", bad)
		}
	}
}

func TestCallback_DispatchesWithChatThreadAndToast(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	var got cbQuery
	b.onCallback("tt", func(_ context.Context, q cbQuery) (string, error) {
		got = q
		return "ok!", nil
	})

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 7, 55, "tt:9"))

	if toast := answeredOnce(t, fake, "q1"); toast != "ok!" {
		t.Fatalf("toast = %q", toast)
	}
	if got.ID != "q1" || got.ChatID != streamChat || got.ThreadID != 7 || got.MessageID != 55 ||
		got.UserID != 42 || got.Chat.Folder != "owner" || got.Data.Act != "tt" ||
		len(got.Data.Args) != 1 || got.Data.Args[0] != "9" {
		t.Fatalf("handler got %+v", got)
	}
}

func TestCallback_UnknownActAnswersExpired(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 5, "zz:1"))
	b.callbackHandler(context.Background(), b.api, cbUpdate("q2", streamChat, 0, 5, ""))

	if toast := answeredOnce(t, fake, "q1"); toast != cbToastExpired {
		t.Fatalf("toast = %q, want %q", toast, cbToastExpired)
	}
	if toast := answeredOnce(t, fake, "q2"); toast != cbToastExpired {
		t.Fatalf("empty data toast = %q", toast)
	}
}

func TestCallback_UnregisteredChatAnswersAndIgnores(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	called := false
	b.onCallback("tt", func(context.Context, cbQuery) (string, error) {
		called = true
		return "", nil
	})

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat+5, 0, 5, "tt"))

	if called {
		t.Fatal("handler ran for an unregistered chat")
	}
	if toast := answeredOnce(t, fake, "q1"); toast != cbToastNotRegistered {
		t.Fatalf("toast = %q", toast)
	}
}

func TestCallback_HandlerErrorStillAnswers(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	b.onCallback("tt", func(context.Context, cbQuery) (string, error) {
		return "", errors.New("boom /home/x token=abc")
	})

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 5, "tt"))

	toast := answeredOnce(t, fake, "q1")
	if toast != cbToastFailed {
		t.Fatalf("toast = %q, want %q", toast, cbToastFailed)
	}
}

func TestCallback_PanicStillAnswers(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	b.onCallback("tt", func(context.Context, cbQuery) (string, error) {
		panic("kaboom")
	})

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 5, "tt"))

	if toast := answeredOnce(t, fake, "q1"); toast != cbToastFailed {
		t.Fatalf("toast = %q", toast)
	}
}

func TestCallback_InaccessibleMessageToastOnly(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	called := false
	b.onCallback("tt", func(context.Context, cbQuery) (string, error) {
		called = true
		return "", nil
	})
	upd := cbUpdate("q1", streamChat, 0, 5, "tt")
	upd.CallbackQuery.Message = models.MaybeInaccessibleMessage{
		Type: models.MaybeInaccessibleMessageTypeInaccessibleMessage,
		InaccessibleMessage: &models.InaccessibleMessage{
			Chat: models.Chat{ID: streamChat}, MessageID: 5,
		},
	}

	b.callbackHandler(context.Background(), b.api, upd)
	// An inline-mode button carries no message at all.
	upd2 := cbUpdate("q2", streamChat, 0, 5, "tt")
	upd2.CallbackQuery.Message = models.MaybeInaccessibleMessage{}
	b.callbackHandler(context.Background(), b.api, upd2)

	if called {
		t.Fatal("handler ran for an inaccessible message")
	}
	for _, id := range []string{"q1", "q2"} {
		if toast := answeredOnce(t, fake, id); toast != cbToastExpired {
			t.Fatalf("%s toast = %q", id, toast)
		}
	}
	if n := len(fake.calls); n != 2 {
		t.Fatalf("API calls = %d, want only the 2 answers", n)
	}
}

// The callback handler is registered for every callback_query and for
// nothing else: messages and draft stops still reach the default handler.
func TestCallbackHandlerLeavesMessagesToDefault(t *testing.T) {
	fake := &fakeTelegram{}
	srvBot := newCallbackFixture(t, fake)
	var seen []string
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	opts := append(srvBot.botOptions(),
		bot.WithServerURL(srv.URL), bot.WithSkipGetMe(), bot.WithNotAsyncHandlers(),
		bot.WithDefaultHandler(func(_ context.Context, _ *bot.Bot, u *models.Update) {
			switch {
			case u.Message != nil:
				seen = append(seen, "message")
			case u.StoppedMessageGeneration != nil:
				seen = append(seen, "stop")
			case u.CallbackQuery != nil:
				seen = append(seen, "callback")
			}
		}))
	api, err := bot.New("123:test", opts...)
	if err != nil {
		t.Fatal(err)
	}
	srvBot.api = api
	srvBot.registerHandlers(api)

	ctx := context.Background()
	api.ProcessUpdate(ctx, &models.Update{Message: &models.Message{ID: 1, Chat: models.Chat{ID: streamChat}, Text: "hello"}})
	srvBot.inboundHandlers.Wait() // join the tracked asynchronous handler
	api.ProcessUpdate(ctx, &models.Update{Message: &models.Message{ID: 2, Chat: models.Chat{ID: streamChat}, Text: "st:1"}})
	srvBot.inboundHandlers.Wait() // join the tracked asynchronous handler
	api.ProcessUpdate(ctx, stopUpdate(streamChat, 3))
	srvBot.inboundHandlers.Wait() // join the tracked asynchronous handler
	api.ProcessUpdate(ctx, cbUpdate("q1", streamChat, 0, 5, "zz"))

	srvBot.inboundHandlers.Wait()
	if strings.Join(seen, ",") != "message,message,stop" {
		t.Fatalf("default handler saw %v", seen)
	}
	if answeredOnce(t, fake, "q1") != cbToastExpired {
		t.Fatal("callback not answered by the dispatcher")
	}
}

func TestAllowedUpdatesExplicit(t *testing.T) {
	want := map[string]bool{"message": true, "callback_query": true, "stopped_message_generation": true}
	if len(allowedUpdates) != len(want) {
		t.Fatalf("allowedUpdates = %v", allowedUpdates)
	}
	for _, u := range allowedUpdates {
		if !want[u] {
			t.Fatalf("unexpected allowed update %q", u)
		}
	}
}
