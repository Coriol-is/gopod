package telegram

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

const streamChat = int64(1001)

// sentMessage is a sendMessage/editMessageText result with the given id.
func sentMessage(id string) (int, string) {
	return http.StatusOK, `{"ok":true,"result":{"message_id":` + id +
		`,"date":0,"chat":{"id":1001,"type":"private"}}}`
}

// newStreamFixture returns a fake-API Bot with a real store and a
// recorded turn, plus the queue item for that turn.
func newStreamFixture(t *testing.T, fake *fakeTelegram) (*Bot, queue.Item) {
	t.Helper()
	ctx := context.Background()
	b := newFakeBot(t, fake)
	b.streamTick = 10 * time.Millisecond
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"),
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	b.store = st
	id, _, err := st.InsertTurn(ctx, store.Turn{
		Source: "telegram", SourceID: "1001:10", ChatFolder: "owner",
		ChatID: streamChat, TGMessageID: 10, Text: "hi",
	})
	if err != nil {
		t.Fatalf("InsertTurn: %v", err)
	}
	return b, queue.Item{ID: id, ChatID: streamChat, MessageID: 10, Folder: "owner", Text: "hi"}
}

// waitFor polls cond until it holds or the test times out.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// draftShowing reports whether some draft call carried text.
func draftShowing(fake *fakeTelegram, text string) func() bool {
	return func() bool {
		for _, c := range fake.callsTo("sendMessageDraft") {
			if strings.HasPrefix(stripHeader(c.Form["text"]), text) {
				return true
			}
		}
		return false
	}
}

func TestRunAgentStreaming_UsesDraftsThenFinalMessage(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage("555")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	done := make(chan struct{})
	go func() {
		b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
		close(done)
	}()

	acc := ""
	for _, c := range []string{"**one**", " two", " three"} {
		chunks <- c
		acc += c
		waitFor(t, "draft "+acc, draftShowing(fake, acc))
	}
	close(chunks)
	result <- runner.RunResult{FullText: acc}
	<-done

	drafts := fake.callsTo("sendMessageDraft")
	if drafts[0].Form["text"] == "" {
		t.Error("opening draft must not send empty text")
	}
	prev := ""
	for _, d := range drafts {
		text := strings.TrimSuffix(d.Form["text"], "❘")
		if !strings.HasPrefix(text, prev) {
			t.Errorf("draft text shrank: %q after %q", text, prev)
		}
		prev = text
		if d.Form["draft_id"] != drafts[0].Form["draft_id"] || d.Form["draft_id"] == "0" {
			t.Errorf("draft ids must be one non-zero id per turn, got %q then %q",
				drafts[0].Form["draft_id"], d.Form["draft_id"])
		}
	}

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want exactly 1: %+v", len(sends), sends)
	}
	if sends[0].Form["parse_mode"] != "HTML" || !strings.Contains(sends[0].Form["text"], "<b>one</b>") {
		t.Errorf("final message not HTML: %v", sends[0].Form)
	}
	if n := len(fake.callsTo("editMessageText")); n != 0 {
		t.Errorf("editMessageText calls = %d, want 0 on the draft path", n)
	}

	turn, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if turn.ReplyMsgID != 555 {
		t.Errorf("ReplyMsgID = %d, want 555", turn.ReplyMsgID)
	}
	if turn.PlaceholderMsgID != 0 {
		t.Errorf("PlaceholderMsgID = %d, want 0 (drafts have no message)", turn.PlaceholderMsgID)
	}
}

func TestRunAgentStreaming_DraftUnsupportedFallsBackToEdits(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case "sendMessage", "editMessageText":
			return sentMessage("77")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	done := make(chan struct{})
	go func() {
		b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
		close(done)
	}()

	chunks <- "**hello**"
	waitFor(t, "edit with partial text", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if strings.HasPrefix(c.Form["text"], "**hello**") {
				return true
			}
		}
		return false
	})
	close(chunks)
	result <- runner.RunResult{FullText: "**hello**"}
	<-done

	if n := len(fake.callsTo("sendMessageDraft")); n != 1 {
		t.Errorf("sendMessageDraft calls = %d, want 1 (no retries after refusal)", n)
	}
	if b.draftsSupported(streamChat) {
		t.Error("chat must be marked unsupported")
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1 (the placeholder): %+v", len(sends), sends)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if final.Form["parse_mode"] != "HTML" || !strings.Contains(final.Form["text"], "<b>hello</b>") {
		t.Errorf("final edit not HTML: %v", final.Form)
	}

	turn, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if turn.PlaceholderMsgID != 77 || turn.ReplyMsgID != 77 {
		t.Errorf("turn placeholder/reply = %d/%d, want 77/77", turn.PlaceholderMsgID, turn.ReplyMsgID)
	}
}

func TestRunAgentStreaming_DraftOverflowStartsNewDraftAndSendsWholeReply(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage("600")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	done := make(chan struct{})
	go func() {
		b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
		close(done)
	}()

	big := strings.Repeat("a", 4001)
	chunks <- big
	chunks <- "tail"
	waitFor(t, "continuation draft", draftShowing(fake, "tail"))
	close(chunks)
	result <- runner.RunResult{FullText: big + "tail"}
	<-done

	drafts := fake.callsTo("sendMessageDraft")
	if first, last := drafts[0].Form["draft_id"], drafts[len(drafts)-1].Form["draft_id"]; first == last {
		t.Errorf("overflow must continue in a new draft id, both %q", first)
	}
	for _, d := range drafts {
		if len(d.Form["text"]) > 4096 {
			t.Errorf("draft text %d bytes exceeds the 4096 limit", len(d.Form["text"]))
		}
	}
	var got strings.Builder
	for _, s := range fake.callsTo("sendMessage") {
		got.WriteString(s.Form["text"])
	}
	if got.String() != big+"tail" {
		t.Errorf("final messages carry %d bytes, want the whole reply (%d bytes) once", got.Len(), len(big)+4)
	}
	if n := len(fake.callsTo("editMessageText")); n != 0 {
		t.Errorf("editMessageText calls = %d, want 0", n)
	}
}

func TestRunAgentStreaming_OpeningDraftBadCallUsesEditsThisTurnOnly(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: chat not found")
		case "sendMessage", "editMessageText":
			return sentMessage("88")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	close(chunks)
	result <- runner.RunResult{FullText: "done"}
	b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})

	drafts := fake.callsTo("sendMessageDraft")
	if len(drafts) != 1 || drafts[0].Form["text"] == "" {
		t.Fatalf("want one non-empty opening draft, got %+v", drafts)
	}
	if !b.draftsSupported(streamChat) {
		t.Error("a single bad call must not mark the chat unsupported")
	}
	if n := len(fake.callsTo("sendMessage")); n != 1 {
		t.Errorf("sendMessage calls = %d, want 1 (the placeholder)", n)
	}
	turn, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if turn.ReplyMsgID != 88 {
		t.Errorf("ReplyMsgID = %d, want 88", turn.ReplyMsgID)
	}
}
