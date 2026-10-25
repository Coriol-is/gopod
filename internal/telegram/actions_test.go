package telegram

import (
	"context"
	"fmt"
	"strings"
	"testing"

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

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 7, 99, fmt.Sprintf("rt:%d", id)))

	if toast := answeredOnce(t, fake, "q1"); toast != retryToastQueued {
		t.Fatalf("toast = %q", toast)
	}
	got := mt.inserted()
	if len(got) != 1 {
		t.Fatalf("enqueued %d turns, want 1", len(got))
	}
	it := got[0]
	if it.Text != "original question" || it.ThreadID != 7 || it.ChatID != streamChat ||
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
