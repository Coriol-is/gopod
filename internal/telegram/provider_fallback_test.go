package telegram

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func quotaFixture(t *testing.T) (*Bot, *fakeTelegram, queue.Item, *memTurns, *int) {
	t.Helper()
	b, fake, r := newCommandFixture(t)
	mt := withQueue(t, b)
	switched := new(int)
	control.RegisterProviderFallbackCommand(r, b.store, func(context.Context, string, string, string) error { *switched++; return nil }, b.EnqueueProviderFallback)
	id, _, err := b.store.InsertTurn(context.Background(), store.Turn{Source: "telegram", SourceID: "quota:500", ChatFolder: "owner", ChatID: streamChat, TGMessageID: 500, ThreadID: 7, IsOwner: true, IsVoice: true, FilePath: "/workspace/chat/uploads/voice", Text: "voice transcript"})
	if err != nil {
		t.Fatal(err)
	}
	return b, fake, queue.Item{ID: id, ChatID: streamChat, MessageID: 500, ThreadID: 7, Folder: "owner", IsOwner: true, IsVoice: true, FilePath: "/workspace/chat/uploads/voice", Text: "voice transcript"}, mt, switched
}

func TestQuotaOfferWaitsForConfirmationAndRetriesOnceSameThread(t *testing.T) {
	b, fake, item, mt, switched := quotaFixture(t)
	b.replyError(context.Background(), item, &runner.UsageLimitError{Provider: "claude"})
	send := lastCall(t, fake, "sendMessage")
	buttons := buttonsOf(t, send)
	if len(buttons) != 2 || buttons[0].Text != btnSwitchContinue || buttons[1].Text != btnFallbackCancel || !strings.HasPrefix(buttons[0].CallbackData, "cf:") {
		t.Fatalf("buttons: %+v", buttons)
	}
	if !strings.Contains(send.Form["text"], "весь чат") || !strings.Contains(send.Form["text"], "codex") || send.Form["message_thread_id"] != "7" {
		t.Fatalf("offer: %v", send.Form)
	}
	if *switched != 0 || len(mt.inserted()) != 0 {
		t.Fatal("switched before confirmation")
	}
	// The confirmation store spends the pair atomically under a double tap.
	var wg sync.WaitGroup
	for _, id := range []string{"yes-one", "yes-two"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			b.callbackHandler(context.Background(), b.api, cbUpdate(id, streamChat, 7, 600, buttons[0].CallbackData))
		}(id)
	}
	wg.Wait()
	answeredOnce(t, fake, "yes-one")
	answeredOnce(t, fake, "yes-two")
	got := mt.inserted()
	if *switched != 1 || len(got) != 1 {
		t.Fatalf("switch=%d retries=%d", *switched, len(got))
	}
	if got[0].ThreadID != 7 || got[0].Text != item.Text || got[0].FilePath != item.FilePath || !got[0].IsVoice || got[0].SourceID != control.FallbackSourceID(item.ID) {
		t.Fatalf("metadata lost: %+v", got[0])
	}
	edit := lastCall(t, fake, "editMessageText")
	if !strings.Contains(edit.Form["text"], "Переключено на codex") {
		t.Fatalf("no switch notice: %v", edit.Form)
	}
}

func TestQuotaOfferCancelExpiryAndForeignChatDoNothing(t *testing.T) {
	for _, action := range []string{"cancel", "expired", "foreign"} {
		t.Run(action, func(t *testing.T) {
			b, fake, item, mt, switched := quotaFixture(t)
			now := time.Now()
			b.now = func() time.Time { return now }
			b.replyError(context.Background(), item, &runner.UsageLimitError{Provider: "claude"})
			buttons := buttonsOf(t, lastCall(t, fake, "sendMessage"))
			data := buttons[0].CallbackData
			chat := streamChat
			if action == "cancel" {
				data = buttons[1].CallbackData
			}
			if action == "expired" {
				now = now.Add(confirmTTL + time.Second)
			}
			if action == "foreign" {
				chat = groupChat
			}
			b.callbackHandler(context.Background(), b.api, cbUpdate("action", chat, 7, 600, data))
			answeredOnce(t, fake, "action")
			if *switched != 0 || len(mt.inserted()) != 0 {
				t.Fatalf("%s switched", action)
			}
		})
	}
}

func TestQuotaFallbackSecondLimitReportsWithoutAnotherSwitchOffer(t *testing.T) {
	b, fake, item, _, _ := quotaFixture(t)
	b.replyError(context.Background(), item, &runner.UsageLimitError{Provider: "claude"})
	original, _ := b.store.GetTurn(context.Background(), item.ID)
	original.ID = 0
	original.SourceID = control.FallbackSourceID(item.ID)
	original.TGMessageID++
	id, _, err := b.store.InsertTurn(context.Background(), original)
	if err != nil {
		t.Fatal(err)
	}
	item.ID = id
	b.replyError(context.Background(), item, &runner.UsageLimitError{Provider: "codex"})
	if len(fake.callsTo("sendMessage")) != 2 {
		t.Fatal("second quota notice suppressed")
	}
	last := lastCall(t, fake, "sendMessage")
	if last.Form["text"] != errTextUsageLimit {
		t.Fatalf("second quota: %v", last.Form)
	}
	for _, button := range buttonsOf(t, last) {
		if strings.HasPrefix(button.CallbackData, "cf:") {
			t.Fatal("reverse switch offered")
		}
	}
}

func TestOrdinaryFailureDoesNotOfferProviderSwitch(t *testing.T) {
	b, fake, item, _, switched := quotaFixture(t)
	b.replyError(context.Background(), item, errors.New("temporary 429 proxy error"))
	for _, button := range buttonsOf(t, lastCall(t, fake, "sendMessage")) {
		if strings.HasPrefix(button.CallbackData, "cf:") {
			t.Fatal("ordinary error offered switch")
		}
	}
	if *switched != 0 {
		t.Fatal("ordinary error switched")
	}
}

func TestQuotaFallbackInternalCommandNotPublished(t *testing.T) {
	b, fake, _, _, _ := quotaFixture(t)
	b.router.Register("ping", "ping", "check bot", control.PermPublic, func(context.Context, control.Command) (control.Response, error) { return control.Response{}, nil })
	b.publishCommands(context.Background())
	call := lastCall(t, fake, "setMyCommands")
	if strings.Contains(call.Form["commands"], `"command":""`) || strings.Contains(call.Form["commands"], "quota") {
		t.Fatalf("published internal command: %v", call.Form)
	}
	if !strings.Contains(call.Form["commands"], "ping") {
		t.Fatalf("public command lost: %v", call.Form)
	}
}
