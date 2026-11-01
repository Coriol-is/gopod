package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// setSilent turns silent mode on for the fixture's chat (D055).
func setSilent(t *testing.T, b *Bot, item queue.Item) {
	t.Helper()
	if err := b.store.SetState(context.Background(), control.SilentStateKey+item.Folder, "1"); err != nil {
		t.Fatal(err)
	}
}

// anyCallHas reports whether any call to method carries text in its form.
func anyCallHas(fake *fakeTelegram, method, text string) bool {
	for _, c := range fake.callsTo(method) {
		if strings.Contains(c.Form["text"], text) {
			return true
		}
	}
	return false
}

// Silent mode: steps keep showing after answer text; the final message
// carries the answer.
func TestStreamReply_SilentKeepsProgressUntilFinal(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "601")
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "go test ./..."}
	waitFor(t, "first step", draftShowing(fake, "⚙️ go test ./...\n❘"))
	s.chunks <- "All tests "
	s.chunks <- "pass."
	// The runner hands the step over only after the chunks (Progress is
	// unbuffered), so the draft that shows it proves ticks ran with text
	// in hand.
	s.progress <- runner.ProgressEvent{Tool: "Read", Detail: "/workspace/chat/main.go"}
	waitFor(t, "two steps", draftShowing(fake, "⚙️ go test ./...\n📖 main.go\n❘"))
	if anyCallHas(fake, "sendMessageDraft", "All tests") {
		t.Errorf("silent draft showed answer text: %q", lastDraftText(fake))
	}
	s.chunks <- " Really."
	s.finish("All tests pass.")
	if err := <-done; err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Form["text"], "All tests pass.") ||
		strings.Contains(sends[0].Form["text"], "go test") {
		t.Fatalf("final = %+v, want the answer only", sends)
	}
	if anyCallHas(fake, "sendMessageDraft", "All tests") {
		t.Errorf("answer text reached a draft: %q", lastDraftText(fake))
	}
}

// Silent mode on the edit path: the placeholder shows steps only; the
// final edit is the answer.
func TestStreamReply_SilentEditPathShowsStepsOnly(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case "sendMessage", "editMessageText":
			return sentMessage("78")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Edit", Detail: "/workspace/chat/a.go"}
	waitFor(t, "placeholder with step", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if stripHeader(c.Form["text"]) == "✏️ a.go\n❘" {
				return true
			}
		}
		return false
	})
	s.chunks <- "**Fixed** a.go"
	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "go vet"}
	waitFor(t, "second step", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if stripHeader(c.Form["text"]) == "✏️ a.go\n⚙️ go vet\n❘" {
				return true
			}
		}
		return false
	})
	if anyCallHas(fake, "editMessageText", "Fixed") {
		t.Error("silent placeholder showed answer text before the final edit")
	}
	s.finish("**Fixed** a.go")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1]
	if final.Form["parse_mode"] != "HTML" || !strings.Contains(final.Form["text"], "<b>Fixed</b>") || strings.Contains(final.Form["text"], "✏️") {
		t.Errorf("final edit = %v, want the answer replacing the steps", final.Form)
	}
	if n := len(fake.callsTo("sendMessage")); n != 1 {
		t.Errorf("sendMessage calls = %d, want 1 (the placeholder)", n)
	}
}

// Silent mode with a plain run (no Progress channel): the draft never
// shows the text; the final message does.
func TestStreamReply_SilentPlainRunShowsHeaderOnly(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "602")
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	chunks := make(chan string, 8)
	result := make(chan runner.RunResult, 1)
	stream := &runner.AgentStream{Chunks: chunks, Result: result}
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, stream) }()

	chunks <- "Hello"
	// No step ever arrives, so the only positive signals are the opening
	// draft and the final send; a broken silent mode would draft the
	// chunk on one of the ticks (10 ms) in between.
	waitFor(t, "opening draft", func() bool { return len(fake.callsTo("sendMessageDraft")) >= 1 })
	time.Sleep(100 * time.Millisecond)
	if anyCallHas(fake, "sendMessageDraft", "Hello") {
		t.Errorf("silent draft showed plain-run text: %q", lastDraftText(fake))
	}
	result <- runner.RunResult{FullText: "Hello"}
	close(result)
	close(chunks)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Form["text"], "Hello") {
		t.Fatalf("final = %+v, want Hello", sends)
	}
	if anyCallHas(fake, "sendMessageDraft", "Hello") {
		t.Errorf("plain-run text reached a draft: %q", lastDraftText(fake))
	}
}

// Stop in silent mode after text arrived delivers that text (D021).
func TestStreamReply_SilentStopDeliversPartialText(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "603")
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "ls"}
	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "cat x"}
	waitFor(t, "two steps", draftShowing(fake, "⚙️ ls\n⚙️ cat x"))
	// Text after the last step is the answer (D024); a later step would
	// drop it as narration, so the ticks (10 ms) are the only signal.
	s.chunks <- "Half an answer"
	time.Sleep(100 * time.Millisecond)
	if anyCallHas(fake, "sendMessageDraft", "Half") {
		t.Error("silent draft showed answer text")
	}
	b.bindDraft(item.ID, item.ChatID, 4343)
	b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, 4343))
	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	go s.finish("x")
	if got := fake.callsTo("sendMessage")[0].Form["text"]; !strings.Contains(got, "Half an answer") || strings.Contains(got, "⚙️") {
		t.Errorf("stopped reply = %q", got)
	}
}

// Silent mode on the edit path with a reply that does not fit one
// message: the placeholder keeps the progress summary and the reply
// goes out as separate messages, nothing is lost to a rejected edit.
func TestStreamReply_SilentEditPathLongReplySentSeparately(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, form map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case "sendMessage", "editMessageText":
			if len(form["text"]) > 4096 {
				return apiError(http.StatusBadRequest, "Bad Request: message is too long")
			}
			return sentMessage("79")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "make"}
	waitFor(t, "placeholder with step", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if stripHeader(c.Form["text"]) == "⚙️ make\n❘" {
				return true
			}
		}
		return false
	})
	long := strings.Repeat("Line of the answer number one.\n", 200) + "THE END"
	s.chunks <- long
	s.finish(long)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1].Form["text"]
	if !strings.Contains(final, "⚙️ make") || strings.Contains(final, "❘") || strings.Contains(final, "Line of") {
		t.Errorf("placeholder final = %q, want the progress summary without cursor", final)
	}
	var sentText strings.Builder
	sends := fake.callsTo("sendMessage")
	for _, c := range sends[1:] { // sends[0] is the placeholder
		sentText.WriteString(c.Form["text"])
	}
	if len(sends) < 2 || !strings.Contains(sentText.String(), "THE END") || strings.Count(sentText.String(), "Line of") != 200 {
		t.Errorf("reply sends = %d, text has %d lines and end=%v; want the whole reply",
			len(sends)-1, strings.Count(sentText.String(), "Line of"), strings.Contains(sentText.String(), "THE END"))
	}
}

// Stop in silent mode on the edit path with a long partial: same split.
func TestStreamReply_SilentEditPathStopLongPartial(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, form map[string]string) (int, string) {
		switch method {
		case "sendMessageDraft":
			return apiError(http.StatusBadRequest, "Bad Request: method is not available")
		case "sendMessage", "editMessageText":
			if len(form["text"]) > 4096 {
				return apiError(http.StatusBadRequest, "Bad Request: message is too long")
			}
			return sentMessage("80")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	setSilent(t, b, item)
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "make"}
	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "make test"}
	waitFor(t, "two steps", func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if stripHeader(c.Form["text"]) == "⚙️ make\n⚙️ make test\n❘" {
				return true
			}
		}
		return false
	})
	long := strings.Repeat("Partial line.\n", 400) + "PARTIAL END"
	s.chunks <- long
	time.Sleep(100 * time.Millisecond)
	if anyCallHas(fake, "editMessageText", "Partial line") {
		t.Error("silent placeholder showed answer text")
	}
	if !b.stopTurn(item.ID, item.ChatID) {
		t.Fatal("stopTurn refused")
	}
	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	go s.finish("x")
	var sentText strings.Builder
	for _, c := range fake.callsTo("sendMessage")[1:] {
		sentText.WriteString(c.Form["text"])
	}
	if !strings.Contains(sentText.String(), "PARTIAL END") {
		t.Errorf("stopped reply sends = %q, want the partial text", sentText.String())
	}
}
