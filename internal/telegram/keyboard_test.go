package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// editPathFake answers like a chat that refuses drafts: placeholders and
// edits succeed with message id 77.
func editPathFake() *fakeTelegram {
	return &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case "sendMessage", "editMessageText":
			return sentMessage("77")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// markupData returns the callback data of every button in a call's
// reply_markup, and whether the call carried a reply_markup at all.
func markupData(t *testing.T, c apiCall) ([]string, bool) {
	t.Helper()
	raw, ok := c.Form["reply_markup"]
	if !ok {
		return nil, false
	}
	var kb models.InlineKeyboardMarkup
	if err := json.Unmarshal([]byte(raw), &kb); err != nil {
		t.Fatalf("reply_markup %q: %v", raw, err)
	}
	var out []string
	for _, row := range kb.InlineKeyboard {
		for _, btn := range row {
			out = append(out, btn.CallbackData)
		}
	}
	return out, true
}

func hasStop(t *testing.T, c apiCall, turnID int64) bool {
	data, _ := markupData(t, c)
	return len(data) == 1 && data[0] == "st:"+strconv.FormatInt(turnID, 10)
}

// cleared reports a call that explicitly removes the inline keyboard.
func cleared(t *testing.T, c apiCall) bool {
	data, ok := markupData(t, c)
	return ok && len(data) == 0
}

func TestStopKeyboard(t *testing.T) {
	kb := stopKeyboard(123)
	if len(kb.InlineKeyboard) != 1 || len(kb.InlineKeyboard[0]) != 1 {
		t.Fatalf("keyboard = %+v", kb)
	}
	btn := kb.InlineKeyboard[0][0]
	if btn.CallbackData != "st:123" || btn.Style != "danger" || !strings.Contains(btn.Text, "⏹") {
		t.Fatalf("button = %+v", btn)
	}
	big := stopKeyboard(1<<62 + 12345)
	if len(big.InlineKeyboard[0][0].CallbackData) > cbMaxBytes {
		t.Fatal("stop data over 64 bytes")
	}
}

func TestPlaceholderCarriesStopButton(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, func() {})()
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	s.chunks <- "partial"
	waitFor(t, "edit with text", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if strings.HasPrefix(c.Form["text"], "partial") {
				return true
			}
		}
		return false
	})
	s.finish("partial and more")
	if err := <-done; err != nil {
		t.Fatal(err)
	}

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !hasStop(t, sends[0], item.ID) {
		t.Fatalf("placeholder = %+v, want one send with the stop button", sends)
	}
	edits := fake.callsTo("editMessageText")
	for _, e := range edits[:len(edits)-1] {
		if !hasStop(t, e, item.ID) {
			t.Fatalf("mid-stream edit without the stop button (would flicker or drop it): %v", e.Form)
		}
	}
	if n := len(fake.callsTo("editMessageReplyMarkup")); n != 0 {
		t.Fatalf("keyboard edited separately %d times, want it in the text edits", n)
	}
}

func TestPlaceholderWithoutInflightHasNoStop(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	s := newStructuredStream()
	go s.finish("done")
	if err := b.streamReply(context.Background(), item, s.stream()); err != nil {
		t.Fatal(err)
	}
	if data, _ := markupData(t, fake.callsTo("sendMessage")[0]); len(data) != 0 {
		t.Fatalf("unstoppable turn got buttons %v", data)
	}
}

func TestFinalEditRemovesKeyboard(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, func() {})()
	s := newStructuredStream()
	go s.finish("**done**")
	if err := b.streamReply(context.Background(), item, s.stream()); err != nil {
		t.Fatal(err)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if !strings.Contains(final.Form["text"], "<b>done</b>") || !cleared(t, final) {
		t.Fatalf("final edit = %v, want the reply with the keyboard removed", final.Form)
	}
}

func TestStopCallbackCancelsTurnInSameChat(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer b.registerInflight(5, streamChat, cancel)()

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 77, "st:5"))

	if ctx.Err() == nil {
		t.Fatal("stop button did not cancel the turn")
	}
	if toast := answeredOnce(t, fake, "q1"); toast != stopToastStopping {
		t.Fatalf("toast = %q", toast)
	}
	select {
	case <-b.inflightStop(5):
	default:
		t.Fatal("turn not marked stopped")
	}
}

func TestStopCallbackFromOtherChatIsRefused(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	other := streamChat + 9
	if err := b.store.RegisterChat(context.Background(), store.RegisteredChat{
		JID: buildChatJID(other), Name: "other", Folder: "other", AddedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer b.registerInflight(5, streamChat, cancel)()

	// Someone forwards the callback data from another registered chat.
	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", other, 0, 77, "st:5"))

	if ctx.Err() != nil {
		t.Fatal("a button in another chat stopped this chat's turn")
	}
	if toast := answeredOnce(t, fake, "q1"); toast != stopToastFinished {
		t.Fatalf("toast = %q", toast)
	}
}

func TestStopCallbackAfterFinishToastsFinished(t *testing.T) {
	fake := &fakeTelegram{}
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	cancels := 0
	unregister := b.registerInflight(5, streamChat, func() { cancels++ })

	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 77, "st:5"))
	b.callbackHandler(context.Background(), b.api, cbUpdate("q2", streamChat, 0, 77, "st:5"))
	unregister()
	b.callbackHandler(context.Background(), b.api, cbUpdate("q3", streamChat, 0, 77, "st:5"))
	b.callbackHandler(context.Background(), b.api, cbUpdate("q4", streamChat, 0, 77, "st:x"))

	if cancels != 1 {
		t.Fatalf("cancel called %d times, want 1", cancels)
	}
	if got := answeredOnce(t, fake, "q1"); got != stopToastStopping {
		t.Fatalf("first press toast = %q", got)
	}
	for _, id := range []string{"q2", "q3"} {
		if got := answeredOnce(t, fake, id); got != stopToastFinished {
			t.Fatalf("%s toast = %q, want %q", id, got, stopToastFinished)
		}
	}
	if got := answeredOnce(t, fake, "q4"); got != cbToastExpired {
		t.Fatalf("bad id toast = %q", got)
	}
}

// Stop pressed on the placeholder: the turn ends stopped with the text
// so far in the placeholder, keyboard removed, no second message.
func TestStopCallbackEndsEditPathTurnStopped(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	b.ownerChatID = streamChat
	b.registerCallbacks()
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	defer b.registerInflight(item.ID, item.ChatID, cancelRun)()
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.chunks <- "Half an answer"
	waitFor(t, "partial edit", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if strings.HasPrefix(c.Form["text"], "Half an answer") {
				return true
			}
		}
		return false
	})
	data := "st:" + strconv.FormatInt(item.ID, 10)
	b.callbackHandler(context.Background(), b.api, cbUpdate("q1", streamChat, 0, 77, data))
	if runCtx.Err() == nil {
		t.Fatal("run not cancelled")
	}
	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	go s.finish("late")

	if n := len(fake.callsTo("sendMessage")); n != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (the placeholder only)", n)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if !strings.Contains(final.Form["text"], "Half an answer") || strings.Contains(final.Form["text"], "❘") || !cleared(t, final) {
		t.Fatalf("final edit = %v", final.Form)
	}
	turn, _ := b.store.GetTurn(context.Background(), item.ID)
	if turn.ReplyMsgID != 77 {
		t.Fatalf("reply id = %d, want the placeholder 77", turn.ReplyMsgID)
	}
}

func TestWaitPlaceholderCarriesStopButton(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(streamChat)
	defer b.registerInflight(item.ID, item.ChatID, func() {})()
	ctx := context.Background()

	b.waitNotice(ctx, item)(runner.WaitSlot)

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || sends[0].Form["text"] != waitSlotText || !hasStop(t, sends[0], item.ID) {
		t.Fatalf("wait placeholder = %+v", sends)
	}
	// Taken over by the stream: back to the cursor, Stop kept.
	if ph := b.takeWaitPlaceholder(ctx, item); ph != 88 {
		t.Fatalf("placeholder = %d", ph)
	}
	edits := fake.callsTo("editMessageText")
	if len(edits) != 1 || !hasStop(t, edits[0], item.ID) {
		t.Fatalf("takeover edit = %+v", edits)
	}
}

// Stopped while waiting for the gate: the wait placeholder becomes the
// stopped reply and loses its keyboard.
func TestStreamNotStartedClearsStopButton(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(streamChat)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer b.registerInflight(item.ID, item.ChatID, cancelRun)()
	ctx := context.Background()
	b.waitNotice(ctx, item)(runner.WaitSlot)
	cancelRun()

	if handled, err := b.streamNotStarted(ctx, runCtx, item); !handled || !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("streamNotStarted = %v, %v", handled, err)
	}
	edits := fake.callsTo("editMessageText")
	if len(edits) != 1 || edits[0].Form["text"] != stoppedEmpty || !cleared(t, edits[0]) {
		t.Fatalf("stopped edit = %+v", edits)
	}
}
