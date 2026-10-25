package telegram

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// seqFake answers every sendMessage with a fresh id (500, 501, ...),
// edits with 77 and everything else with ok.
func seqFake() *fakeTelegram {
	var n atomic.Int64
	n.Store(499)
	return &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessage", "sendDocument":
			return sentMessage(strconv.FormatInt(n.Add(1), 10))
		case "editMessageText":
			return sentMessage("77")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// hasAct reports a call whose reply_markup has a button with act.
func hasAct(t *testing.T, c apiCall, act string) bool {
	t.Helper()
	data, _ := markupData(t, c)
	for _, d := range data {
		if strings.HasPrefix(d, act+":") {
			return true
		}
	}
	return false
}

// clearsOf returns the message ids whose keyboard was cleared.
func clearsOf(t *testing.T, fake *fakeTelegram) []string {
	t.Helper()
	var ids []string
	for _, c := range fake.callsTo("editMessageReplyMarkup") {
		if cleared(t, c) {
			ids = append(ids, c.Form["message_id"])
		}
	}
	return ids
}

// queueTurn is a telegram turn row of chatID in thread.
func queueTurn(chatID int64, thread int, msgID int64) store.Turn {
	return store.Turn{
		Source: "telegram", SourceID: strconv.FormatInt(chatID, 10) + ":" + strconv.FormatInt(msgID, 10),
		ChatFolder: "owner", ChatID: chatID, TGMessageID: msgID, ThreadID: thread, Text: "q",
	}
}

// turnIn records a turn of the stream chat in thread and returns its item.
func turnIn(t *testing.T, b *Bot, thread int, msgID int64) queue.Item {
	t.Helper()
	id, _, err := b.store.InsertTurn(context.Background(), queueTurn(streamChat, thread, msgID))
	if err != nil {
		t.Fatal(err)
	}
	return queue.Item{ID: id, ChatID: streamChat, MessageID: int(msgID), ThreadID: thread, Folder: "owner", Text: "q"}
}

func TestSendFinal_KeyboardOnLastChunkOnly(t *testing.T) {
	fake := seqFake()
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "long")
	long := strings.Repeat("Абзац текста ответа.\n\n", 400) // several chunks
	if _, err := b.sendFinal(context.Background(), streamChat, 0, 10, long, &kb); err != nil {
		t.Fatal(err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) < 2 {
		t.Fatalf("sent %d chunks, want several", len(sends))
	}
	for i, c := range sends {
		_, has := markupData(t, c)
		if last := i == len(sends)-1; has != last {
			t.Fatalf("chunk %d of %d: keyboard = %v", i+1, len(sends), has)
		}
	}
	if !hasAct(t, sends[len(sends)-1], "rg") {
		t.Fatal("last chunk lacks the action row")
	}
}

func TestSendFinal_NoKeyboardOnDocuments(t *testing.T) {
	fake := seqFake()
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "x")
	text := "Вот файл:\n\n" + fence("go", 80) + "\nГотово."
	if _, err := b.sendFinal(context.Background(), streamChat, 0, 10, text, &kb); err != nil {
		t.Fatal(err)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 {
		t.Fatalf("documents = %d, want 1", len(docs))
	}
	if _, has := docs[0].Form["reply_markup"]; has {
		t.Fatal("document carries the action row")
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !hasAct(t, sends[0], "rg") {
		t.Fatalf("text reply = %+v, want the action row", sends)
	}
}

// A keyboard Telegram refuses never costs the reply.
func TestSendFinal_RefusedKeyboardStillDelivers(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" {
			if _, has := form["reply_markup"]; has {
				return apiError(400, "Bad Request: BUTTON_TYPE_INVALID")
			}
			return sentMessage("600")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "x")
	id, err := b.sendFinal(context.Background(), streamChat, 0, 10, "ответ", &kb)
	if err != nil || id != 600 {
		t.Fatalf("sendFinal = %d, %v; want the reply without buttons", id, err)
	}
}

func TestNewReplyClearsPreviousKeyboardSameConversation(t *testing.T) {
	fake := seqFake()
	b, _ := newStreamFixture(t, fake)
	first := turnIn(t, b, 0, 11)
	second := turnIn(t, b, 0, 12)

	if err := streamAll(t, b, first, "первый ответ"); err != nil {
		t.Fatal(err)
	}
	sends := fake.callsTo("sendMessage")
	if !hasAct(t, sends[len(sends)-1], "rg") {
		t.Fatal("first reply has no action row")
	}
	if got := clearsOf(t, fake); len(got) != 0 {
		t.Fatalf("cleared %v before any second reply", got)
	}
	if err := streamAll(t, b, second, "второй ответ"); err != nil {
		t.Fatal(err)
	}
	sends = fake.callsTo("sendMessage")
	last := sends[len(sends)-1]
	if !hasAct(t, last, "rg") || !strings.Contains(last.Form["reply_markup"], "rg:"+strconv.FormatInt(second.ID, 10)) {
		t.Fatalf("second reply markup = %s", last.Form["reply_markup"])
	}
	row, _ := b.store.GetTurn(context.Background(), first.ID)
	if got := clearsOf(t, fake); len(got) != 1 || got[0] != strconv.Itoa(row.ReplyMsgID) {
		t.Fatalf("cleared %v, want the first reply %d", got, row.ReplyMsgID)
	}
}

func TestOtherThreadKeyboardUntouched(t *testing.T) {
	fake := seqFake()
	b, _ := newStreamFixture(t, fake)
	if err := streamAll(t, b, turnIn(t, b, 7, 11), "в топике"); err != nil {
		t.Fatal(err)
	}
	if err := streamAll(t, b, turnIn(t, b, 0, 12), "в главном"); err != nil {
		t.Fatal(err)
	}
	if got := clearsOf(t, fake); len(got) != 0 {
		t.Fatalf("a reply in main cleared %v in topic 7", got)
	}
	// The next reply in topic 7 clears topic 7's previous one.
	if err := streamAll(t, b, turnIn(t, b, 7, 13), "снова в топике"); err != nil {
		t.Fatal(err)
	}
	if got := clearsOf(t, fake); len(got) != 1 || got[0] != "500" {
		t.Fatalf("cleared %v, want topic 7's first reply 500", got)
	}
}

func TestStoppedReplyGetsNoRegenerate(t *testing.T) {
	fake := seqFake()
	b, _ := newStreamFixture(t, fake)
	if err := streamAll(t, b, turnIn(t, b, 0, 11), "полный ответ"); err != nil {
		t.Fatal(err)
	}
	item := turnIn(t, b, 0, 12)
	chunks := make(chan string)
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{}
	err := b.finishStopped(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result}, "часть")
	if err != queue.ErrStopped {
		t.Fatalf("err = %v", err)
	}
	sends := fake.callsTo("sendMessage")
	if _, has := markupData(t, sends[len(sends)-1]); has {
		t.Fatalf("stopped reply carries buttons: %s", sends[len(sends)-1].Form["reply_markup"])
	}
	// It is the newest reply now: the previous one's row goes.
	if got := clearsOf(t, fake); len(got) != 1 || got[0] != "500" {
		t.Fatalf("cleared %v, want the previous reply 500", got)
	}
}

// The edit path's placeholder becomes the reply: the final edit swaps
// the Stop button for the action row in the same call.
func TestEditPathFinalCarriesActionRow(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, func() {})()
	s := newStructuredStream()
	go s.finish("готово")
	if err := b.streamReply(context.Background(), item, s.stream()); err != nil {
		t.Fatal(err)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if !hasAct(t, final, "rg") || hasAct(t, final, "st") {
		t.Fatalf("final edit markup = %s", final.Form["reply_markup"])
	}
}

func TestSyncReplyCarriesActionRow(t *testing.T) {
	fake := seqFake()
	b, item := newStreamFixture(t, fake)
	b.deliverSyncReply(context.Background(), item, "ответ")
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !hasAct(t, sends[0], "rg") {
		t.Fatalf("sync reply = %+v", sends)
	}
}

// A turn without a durable id (memory-only queue) has nothing for the
// buttons to name: no row.
func TestNoActionRowWithoutTurnID(t *testing.T) {
	fake := seqFake()
	b, item := newStreamFixture(t, fake)
	item.ID = 0
	b.deliverSyncReply(context.Background(), item, "ответ")
	if _, has := markupData(t, fake.callsTo("sendMessage")[0]); has {
		t.Fatal("reply of a turn without id carries buttons")
	}
}

// On the edit path too, a refused keyboard never costs the reply.
func TestEditFinal_RefusedKeyboardStillEdits(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "editMessageText" {
			if strings.Contains(form["reply_markup"], "rg:") {
				return apiError(400, "Bad Request: BUTTON_TYPE_INVALID")
			}
			return sentMessage("77")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "x")
	if err := b.editMessageHTMLKB(context.Background(), streamChat, 77, "<b>ответ</b>", &kb); err != nil {
		t.Fatalf("edit = %v, want the reply without buttons", err)
	}
	edits := fake.callsTo("editMessageText")
	last := edits[len(edits)-1]
	if last.Form["text"] != "<b>ответ</b>" || !cleared(t, last) {
		t.Fatalf("last edit = %v", last.Form)
	}
}
