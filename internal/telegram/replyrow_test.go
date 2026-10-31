package telegram

import (
	"context"
	"fmt"
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
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()
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

// refusingFake refuses every sendMessage whose keyboard has a reply row
// and answers the others with fresh ids (600, 601, ...).
func refusingFake() *fakeTelegram {
	var n atomic.Int64
	n.Store(599)
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" {
			if strings.Contains(form["reply_markup"], "rg:") {
				return apiError(400, "Bad Request: BUTTON_TYPE_INVALID")
			}
			return sentMessage(strconv.FormatInt(n.Add(1), 10))
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// A refused row on a multi-chunk reply resends only the last chunk,
// as HTML, without the row: nothing twice, formatting kept.
func TestSendFinal_RefusedKeyboardMultiChunkNoDuplicates(t *testing.T) {
	fake := refusingFake()
	b, item := newStreamFixture(t, fake)
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "**Абзац %d** текста ответа.\n\n", i)
	}
	long := sb.String()
	if err := streamAll(t, b, item, long); err != nil {
		t.Fatal(err)
	}
	sends := fake.callsTo("sendMessage")
	chunks := len(sends) - 1 // one refused attempt
	if chunks < 3 {
		t.Fatalf("%d chunks, want at least 3", chunks)
	}
	seen := map[string]bool{}
	for i, c := range sends {
		if c.Form["parse_mode"] != "HTML" || !strings.Contains(c.Form["text"], "<b>") {
			t.Fatalf("call %d not HTML: %.60q", i, c.Form["text"])
		}
		if i == len(sends)-2 {
			continue // the refused attempt; its text goes out once more
		}
		if seen[c.Form["text"]] {
			t.Fatalf("chunk %d delivered twice", i)
		}
		seen[c.Form["text"]] = true
	}
	last := sends[len(sends)-1]
	if _, has := last.Form["reply_markup"]; has || last.Form["text"] != sends[len(sends)-2].Form["text"] {
		t.Fatalf("retry = %v, want the refused chunk without the row", last.Form)
	}
	row, _ := b.store.GetTurn(context.Background(), item.ID)
	if row.ReplyMsgID != 599+chunks {
		t.Fatalf("recorded reply %d, want %d", row.ReplyMsgID, 599+chunks)
	}
}

func TestSendFinal_RefusedKeyboardSingleChunkKeepsHTML(t *testing.T) {
	fake := refusingFake()
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "x")
	id, err := b.sendFinal(context.Background(), streamChat, 0, 10, "**ответ**", &kb)
	if err != nil || id != 600 {
		t.Fatalf("sendFinal = %d, %v", id, err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 2 {
		t.Fatalf("sendMessage calls = %d, want 2", len(sends))
	}
	if c := sends[1]; c.Form["parse_mode"] != "HTML" || c.Form["text"] != "<b>ответ</b>" {
		t.Fatalf("retry = %v, want the HTML text", c.Form)
	}
}

// goneSeqFake: sends into topic 7 fail as "thread not found" once gone
// is set; sendMessage answers with fresh ids (700, 701, ...); drafts are
// refused unless draftsOK.
func goneSeqFake(gone *atomic.Bool, draftsOK bool) *fakeTelegram {
	var n atomic.Int64
	n.Store(699)
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		switch {
		case method == "sendMessageDraft":
			if draftsOK {
				return http.StatusOK, `{"ok":true,"result":true}`
			}
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case form["message_thread_id"] == "7" && gone.Load():
			return apiError(400, "Bad Request: message thread not found")
		case method == "sendMessage":
			return sentMessage(strconv.FormatInt(n.Add(1), 10))
		case method == "editMessageText":
			return sentMessage("77")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// The reply redirected to main from a gone topic has no row, and it is
// main's newest reply: main's previous row (message 700) goes.
func TestTopicGoneReplyInMainHasNoRow(t *testing.T) {
	for _, tc := range []struct {
		name     string
		draftsOK bool
		run      func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, gone *atomic.Bool)
	}{
		{"draft final", true, func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, gone *atomic.Bool) {
			gone.Store(true)
			if err := streamAll(t, b, item, "ответ"); err != nil {
				t.Fatal(err)
			}
		}},
		{"sync", false, func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, gone *atomic.Bool) {
			gone.Store(true)
			b.deliverSyncReply(context.Background(), item, "ответ")
		}},
		{"drainAndSend", false, func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, gone *atomic.Bool) {
			gone.Store(true) // the placeholder fails: drainAndSend
			if err := streamAll(t, b, item, "ответ"); err != nil {
				t.Fatal(err)
			}
		}},
		{"overflow", false, func(t *testing.T, b *Bot, fake *fakeTelegram, item queue.Item, gone *atomic.Bool) {
			text := strings.Repeat("слово ", 800) // over one placeholder
			chunks := make(chan string)
			result := make(chan runner.RunResult, 1)
			errc := make(chan error, 1)
			go func() {
				errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
			}()
			chunks <- text
			waitFor(t, "second placeholder", func() bool {
				in, _ := callsIn(fake, "sendMessage")
				return len(in) == 2
			})
			gone.Store(true)
			close(chunks)
			result <- runner.RunResult{FullText: text}
			close(result)
			if err := <-errc; err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gone atomic.Bool
			fake := goneSeqFake(&gone, tc.draftsOK)
			b, _ := newStreamFixture(t, fake)
			b.deliverSyncReply(context.Background(), turnIn(t, b, 0, 11), "в главном") // 700, with row
			item := turnIn(t, b, 7, 12)

			tc.run(t, b, fake, item, &gone)

			_, inMain := callsIn(fake, "sendMessage")
			if len(inMain) < 2 || !strings.HasPrefix(inMain[1].Form["text"], threadGoneNote) {
				t.Fatalf("main sends = %d, want main's reply then the topic-gone reply", len(inMain))
			}
			for _, c := range inMain[1:] {
				if _, has := c.Form["reply_markup"]; has {
					t.Fatalf("topic-gone reply in main carries %s", c.Form["reply_markup"])
				}
			}
			if got := clearsOf(t, fake); len(got) != 1 || got[0] != "700" {
				t.Fatalf("cleared %v, want main's previous reply 700", got)
			}
			b.rowMu.Lock()
			_, kept := b.rows.last[chatThread{streamChat, 0}]
			b.rowMu.Unlock()
			if kept {
				t.Fatal("main still records a reply with a row")
			}
		})
	}
}

// When the last chunk fails as HTML even without the row, the plain-text
// fallback sends only that chunk: the earlier ones are out already.
func TestSendFinal_LastChunkPlainFallbackSendsOnlyTheRest(t *testing.T) {
	var n atomic.Int64
	n.Store(599)
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" {
			if form["parse_mode"] == "HTML" && strings.Contains(form["text"], "Абзац 499") {
				return apiError(400, "Bad Request: can't parse entities")
			}
			return sentMessage(strconv.FormatInt(n.Add(1), 10))
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b := newFakeBot(t, fake)
	var sb strings.Builder
	for i := range 500 {
		fmt.Fprintf(&sb, "**Абзац %d** текста ответа.\n\n", i)
	}
	kb := replyKeyboard(5, "x")
	if _, err := b.sendFinal(context.Background(), streamChat, 0, 10, sb.String(), &kb); err != nil {
		t.Fatal(err)
	}
	var plain []apiCall
	for _, c := range fake.callsTo("sendMessage") {
		if c.Form["parse_mode"] == "" {
			plain = append(plain, c)
		}
	}
	if len(plain) == 0 {
		t.Fatal("no plain-text fallback")
	}
	for _, c := range plain {
		if strings.Contains(c.Form["text"], "Абзац 0 ") || strings.Contains(c.Form["text"], "<b>") ||
			strings.Contains(c.Form["text"], "**") {
			t.Fatalf("plain fallback resent delivered text or markup: %.80q", c.Form["text"])
		}
	}
	if !strings.Contains(plain[len(plain)-1].Form["text"], "Абзац 499") || !hasAct(t, plain[len(plain)-1], "rg") {
		t.Fatalf("last plain send = %v", plain[len(plain)-1].Form)
	}
}
