package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// goneFake fails every send into thread 7 with the Bot API's "thread
// gone" 400 and answers everything else normally.
func goneFake() *fakeTelegram {
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if form["message_thread_id"] == "7" {
			return apiError(400, "Bad Request: message thread not found")
		}
		if method == "sendMessage" {
			return sentMessage("777")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// callsIn splits recorded calls to method into those sent into thread 7
// and those sent to the main chat.
func callsIn(fake *fakeTelegram, method string) (inThread, inMain []apiCall) {
	for _, c := range fake.callsTo(method) {
		if _, ok := c.Form["message_thread_id"]; ok {
			inThread = append(inThread, c)
		} else {
			inMain = append(inMain, c)
		}
	}
	return inThread, inMain
}

func TestIsThreadGone(t *testing.T) {
	for _, desc := range []string{
		"Bad Request: message thread not found",
		"Bad Request: TOPIC_DELETED",
		"Bad Request: TOPIC_CLOSED",
	} {
		err := errors.New("bad request, " + desc)
		if !isThreadGone(err) {
			t.Errorf("isThreadGone(%q) = false", desc)
		}
		if isDraftCapabilityError(err) {
			t.Errorf("%q classified as a draft capability error", desc)
		}
	}
	for _, desc := range []string{
		"Bad Request: can't parse entities",
		"Bad Request: message to reply not found",
		"Bad Request: chat not found",
	} {
		if isThreadGone(errors.New("bad request, " + desc)) {
			t.Errorf("isThreadGone(%q) = true", desc)
		}
	}
	if isThreadGone(nil) {
		t.Error("isThreadGone(nil) = true")
	}
}

func TestDraftThreadGoneDoesNotFlipDraftsOrCount(t *testing.T) {
	fake := goneFake()
	b := newFakeBot(t, fake)
	ctx := context.Background()
	for i := 0; i < draftBadRequestLimit+1; i++ {
		err := b.sendDraft(ctx, streamChat, 7, 1, "hi")
		if !errors.Is(err, errThreadGone) {
			t.Fatalf("draft %d: err = %v, want errThreadGone", i, err)
		}
	}
	if !b.draftsSupported(streamChat) {
		t.Error("thread-gone 400s switched drafts off for the whole chat")
	}
	if n := b.draftBadReqs[streamChat]; n != 0 {
		t.Errorf("bad-request run = %d, want 0 (thread-gone must not count)", n)
	}
	if n := len(fake.callsTo("sendMessageDraft")); n != 1 {
		t.Errorf("sendMessageDraft calls = %d, want 1 (no retries into a gone thread)", n)
	}
	// Main chat drafts still work.
	if err := b.sendDraft(ctx, streamChat, 0, 2, "hi"); err != nil {
		t.Errorf("main-chat draft: %v", err)
	}
}

func TestDraftOther400StillCounts(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return apiError(400, "Bad Request: something else")
	}}
	b := newFakeBot(t, fake)
	for i := 0; i < draftBadRequestLimit; i++ {
		_ = b.sendDraft(context.Background(), streamChat, 7, 1, "hi")
	}
	if b.draftsSupported(streamChat) {
		t.Error("repeated non-thread-gone 400s must still switch drafts off")
	}
	if n := len(fake.callsTo("sendMessageDraft")); n != draftBadRequestLimit {
		t.Errorf("sendMessageDraft calls = %d, want %d", n, draftBadRequestLimit)
	}
}

func TestStreamThreadGoneDeliversOnceToMain(t *testing.T) {
	fake := goneFake()
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	chunks <- "hello"
	time.Sleep(10 * b.streamTick) // several ticks against the gone thread
	close(chunks)
	result <- runner.RunResult{FullText: "hello"}
	if err := <-errc; err != nil {
		t.Fatalf("streamReply = %v, want nil (turn done, no retry)", err)
	}

	drafts, mainDrafts := callsIn(fake, "sendMessageDraft")
	if len(drafts) != 1 || len(mainDrafts) != 0 {
		t.Errorf("drafts: %d into the gone thread, %d to main; want 1 and 0", len(drafts), len(mainDrafts))
	}
	inThread, inMain := callsIn(fake, "sendMessage")
	if len(inThread) > 1 {
		t.Errorf("sendMessage into the gone thread = %d, want at most 1", len(inThread))
	}
	if len(inMain) != 1 {
		t.Fatalf("sendMessage to main = %d, want exactly 1: %+v", len(inMain), inMain)
	}
	text := inMain[0].Form["text"]
	if !strings.HasPrefix(text, threadGoneNote) || !strings.Contains(text, "hello") {
		t.Errorf("main reply = %q, want note prefix and the reply", text)
	}
	if _, ok := inMain[0].Form["reply_parameters"]; ok {
		t.Error("main reply must not reply to a message in the gone thread")
	}

	turn, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if turn.ReplyMsgID != 777 {
		t.Errorf("ReplyMsgID = %d, want 777", turn.ReplyMsgID)
	}
}

func TestEditPathThreadGoneDeliversToMain(t *testing.T) {
	fake := goneFake()
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7
	b.markDraftsUnsupported(streamChat)

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	close(chunks)
	result <- runner.RunResult{FullText: "hello"}
	if err := b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result}); err != nil {
		t.Fatalf("streamReply = %v", err)
	}
	inThread, inMain := callsIn(fake, "sendMessage")
	if len(inThread) != 1 {
		t.Errorf("placeholder attempts into the gone thread = %d, want 1", len(inThread))
	}
	if len(inMain) != 1 || !strings.HasPrefix(inMain[0].Form["text"], threadGoneNote) {
		t.Fatalf("want one main reply with the note, got %+v", inMain)
	}
}

func TestReplyThreadGoneChunkedGoesToMain(t *testing.T) {
	fake := goneFake()
	b := newFakeBot(t, fake)
	ctx := context.Background()
	long := strings.Repeat("word ", 1500) // two chunks, with the note too

	id := b.replyTo(ctx, streamChat, 7, 10, long)
	if id != 777 {
		t.Errorf("reply id = %d, want 777", id)
	}
	inThread, inMain := callsIn(fake, "sendMessage")
	if len(inThread) != 1 {
		t.Errorf("sends into the gone thread = %d, want 1 (no plain-text retry)", len(inThread))
	}
	if len(inMain) != 2 {
		t.Fatalf("main chunks = %d, want 2", len(inMain))
	}
	if !strings.HasPrefix(inMain[0].Form["text"], threadGoneNote) {
		t.Errorf("first main chunk lacks the note: %q", inMain[0].Form["text"][:60])
	}

	// Known gone: the next reply goes straight to main.
	b.replyTo(ctx, streamChat, 7, 11, "again")
	if inThread2, _ := callsIn(fake, "sendMessage"); len(inThread2) != 1 {
		t.Errorf("sends into the gone thread = %d after it was known gone, want 1", len(inThread2))
	}
}

func TestReplyOther400InThreadUnchanged(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if form["parse_mode"] == "HTML" {
			return apiError(400, "Bad Request: can't parse entities")
		}
		return sentMessage("5")
	}}
	b := newFakeBot(t, fake)
	b.replyTo(context.Background(), streamChat, 7, 10, "hi")
	inThread, inMain := callsIn(fake, "sendMessage")
	if len(inMain) != 0 || len(inThread) != 2 {
		t.Errorf("want HTML then plain fallback both in thread 7, got %d in thread, %d in main",
			len(inThread), len(inMain))
	}
}

func TestTypingAndPlaceholderSkipGoneThread(t *testing.T) {
	fake := goneFake()
	b := newFakeBot(t, fake)
	ctx := context.Background()

	b.startTyping(ctx, streamChat, 7)()
	b.startTyping(ctx, streamChat, 7)()
	if n := len(fake.callsTo("sendChatAction")); n != 1 {
		t.Errorf("sendChatAction calls = %d, want 1 (stop after thread gone)", n)
	}
	if id := b.sendPlaceholder(ctx, streamChat, 7, 10); id != 0 {
		t.Errorf("placeholder id = %d, want 0 in a gone thread", id)
	}
	if n := len(fake.callsTo("sendMessage")); n != 0 {
		t.Errorf("sendMessage calls = %d, want 0 into a known-gone thread", n)
	}

	// Main chat unaffected.
	b.startTyping(ctx, streamChat, 0)()
	if _, inMain := callsIn(fake, "sendChatAction"); len(inMain) != 1 {
		t.Errorf("main typing calls = %d, want 1", len(inMain))
	}
}

func TestInboundMessageRevivesThread(t *testing.T) {
	b := newFakeBot(t, goneFake())
	b.markThreadGone(streamChat, 7)
	b.noteInboundThread(&models.Message{
		Chat:            models.Chat{ID: streamChat, Type: models.ChatTypePrivate},
		MessageThreadID: 7,
	})
	if b.threadGone(streamChat, 7) {
		t.Error("a message arriving in the thread must clear the gone mark")
	}
}

// editFailFake lets placeholders into thread 7 succeed (id 500) and
// answers every editMessageText with editErr, as when the topic was
// deleted after the placeholder went out. Main-chat sends get id 777.
func editFailFake(editErr string) *fakeTelegram {
	return &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		switch method {
		case "editMessageText":
			return apiError(400, editErr)
		case "sendMessage":
			if _, ok := form["message_thread_id"]; ok {
				return sentMessage("500")
			}
			return sentMessage("777")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
}

// runEditPath streams text through the edit path (drafts off) for a
// turn in thread, with a few ticks of edits, and returns streamReply's
// error.
func runEditPath(t *testing.T, b *Bot, item queue.Item, parts ...string) error {
	t.Helper()
	b.markDraftsUnsupported(item.ChatID)
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	for _, p := range parts {
		chunks <- p
		time.Sleep(5 * b.streamTick)
	}
	close(chunks)
	result <- runner.RunResult{FullText: strings.Join(parts, "")}
	select {
	case err := <-errc:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("streamReply did not return")
		return nil
	}
}

func TestEditPathTopicDeletedAfterPlaceholder(t *testing.T) {
	cases := []struct {
		name      string
		thread    int
		editErr   string
		parts     []string
		wantMain  int // sendMessage calls to main
		wantReply int
	}{
		{"edit not found in topic", 7, "Bad Request: message to edit not found", []string{"hello"}, 1, 777},
		{"thread gone on edit", 7, "Bad Request: message thread not found", []string{"hello"}, 1, 777},
		{"not modified is benign", 7, "Bad Request: message is not modified", []string{"hello"}, 0, 500},
		// The reply outgrew the first placeholder; the final edit of
		// the second one (holding "tail") fails.
		{"overflow, edit not found", 7, "Bad Request: message to edit not found",
			[]string{strings.Repeat("word ", 900), "tail"}, 2, 777},
		{"main chat unchanged", 0, "Bad Request: message to edit not found", []string{"hello"}, 0, 777},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake := editFailFake(c.editErr) // main-chat placeholders get id 777
			b, item := newStreamFixture(t, fake)
			item.ThreadID = c.thread
			if err := runEditPath(t, b, item, c.parts...); err != nil {
				t.Fatalf("streamReply = %v, want nil (turn done)", err)
			}

			var mainSends []apiCall
			for _, s := range fake.callsTo("sendMessage") {
				_, inThread := s.Form["message_thread_id"]
				if !inThread && s.Form["text"] != "❘" {
					mainSends = append(mainSends, s)
				}
			}
			if c.thread == 0 {
				mainSends = nil
				for _, s := range fake.callsTo("sendMessage") {
					if s.Form["text"] != "❘" {
						mainSends = append(mainSends, s)
					}
				}
			}
			if len(mainSends) != c.wantMain {
				t.Fatalf("main sends = %d, want %d: %+v", len(mainSends), c.wantMain, mainSends)
			}
			if c.wantMain > 0 && !strings.HasPrefix(mainSends[0].Form["text"], threadGoneNote) {
				t.Errorf("main reply lacks the note: %.80q", mainSends[0].Form["text"])
			}
			turn, err := b.store.GetTurn(context.Background(), item.ID)
			if err != nil {
				t.Fatalf("GetTurn: %v", err)
			}
			if turn.ReplyMsgID != c.wantReply {
				t.Errorf("ReplyMsgID = %d, want %d", turn.ReplyMsgID, c.wantReply)
			}
		})
	}
}

func TestEditPathTopicDeletedMainSendFailsIsUndelivered(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		switch method {
		case "editMessageText":
			return apiError(400, "Bad Request: message to edit not found")
		case "sendMessage":
			if _, ok := form["message_thread_id"]; ok {
				return sentMessage("500")
			}
			return apiError(400, "Bad Request: chat not found")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}}
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7
	if err := runEditPath(t, b, item, "hello"); !errors.Is(err, errReplyUndelivered) {
		t.Fatalf("streamReply = %v, want errReplyUndelivered", err)
	}
	turn, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if turn.ReplyMsgID != 0 {
		t.Errorf("ReplyMsgID = %d, want 0 (nothing delivered)", turn.ReplyMsgID)
	}
}
