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

// Silent mode (D055): answer text never reaches the draft or the
// placeholder; the chat sees progress until the final message.

// silentFixture is a silent-mode turn streaming a structured run.
type silentFixture struct {
	fake *fakeTelegram
	b    *Bot
	item queue.Item
	s    *structuredStream
	done chan error
}

// startSilent turns silent mode on for the fixture's chat and starts
// streamReply on a fresh structured run.
func startSilent(t *testing.T, fake *fakeTelegram) *silentFixture {
	t.Helper()
	b, item := newStreamFixture(t, fake)
	if err := b.store.SetState(context.Background(), control.SilentStateKey+item.Folder, "1"); err != nil {
		t.Fatal(err)
	}
	f := &silentFixture{fake: fake, b: b, item: item, s: newStructuredStream(), done: make(chan error, 1)}
	go func() { f.done <- b.streamReply(context.Background(), item, f.s.stream()) }()
	return f
}

// step sends one tool step.
func (f *silentFixture) step(tool, detail string) {
	f.s.progress <- runner.ProgressEvent{Tool: tool, Detail: detail}
}

// finish ends the run and returns streamReply's error.
func (f *silentFixture) finish(text string) error {
	f.s.finish(text)
	return <-f.done
}

// strictEditPathFake is editPathFake (keyboard_test.go) that, like
// Telegram, rejects a message or edit over 4096 bytes.
func strictEditPathFake() *fakeTelegram {
	fake := editPathFake()
	inner := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		if (method == "sendMessage" || method == "editMessageText") && len(form["text"]) > 4096 {
			return apiError(http.StatusBadRequest, "Bad Request: message is too long")
		}
		return inner(method, form)
	}
	return fake
}

// anyCallHas reports whether any call to method carries text.
func anyCallHas(fake *fakeTelegram, method, text string) bool {
	for _, c := range fake.callsTo(method) {
		if strings.Contains(c.Form["text"], text) {
			return true
		}
	}
	return false
}

// placeholderShowing reports whether some edit of the placeholder showed
// exactly these steps under the header.
func placeholderShowing(fake *fakeTelegram, steps string) func() bool {
	return func() bool {
		for _, c := range fake.callsTo("editMessageText") {
			if stripHeader(c.Form["text"]) == steps {
				return true
			}
		}
		return false
	}
}

// repliesAfterPlaceholder joins the text of every sendMessage after the
// first one (the placeholder).
func repliesAfterPlaceholder(fake *fakeTelegram) string {
	var sb strings.Builder
	for _, c := range fake.callsTo("sendMessage")[1:] {
		sb.WriteString(c.Form["text"])
	}
	return sb.String()
}

// settle lets several ticks (10 ms) run: the only signal when no later
// step may follow (a step would drop the text as narration, D024).
func settle() { time.Sleep(100 * time.Millisecond) }

func TestStreamReply_SilentKeepsProgressUntilFinal(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "601")
	f := startSilent(t, fake)

	f.step("Bash", "go test ./...")
	waitFor(t, "first step", draftShowing(fake, "⚙️ go test ./...\n❘"))
	f.s.chunks <- "All tests "
	f.s.chunks <- "pass."
	// The runner hands the step over only after the chunks (Progress is
	// unbuffered), so the draft that shows it proves ticks ran with text
	// in hand.
	f.step("Read", "/workspace/chat/main.go")
	waitFor(t, "two steps", draftShowing(fake, "⚙️ go test ./...\n📖 main.go\n❘"))
	if anyCallHas(fake, "sendMessageDraft", "All tests") {
		t.Errorf("silent draft showed answer text: %q", lastDraftText(fake))
	}
	f.s.chunks <- " Really."
	if err := f.finish("All tests pass."); err != nil {
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

// Edit path: the placeholder shows steps only; the final edit is the answer.
func TestStreamReply_SilentEditPathShowsStepsOnly(t *testing.T) {
	fake := strictEditPathFake()
	f := startSilent(t, fake)

	f.step("Edit", "/workspace/chat/a.go")
	waitFor(t, "placeholder with step", placeholderShowing(fake, "✏️ a.go\n❘"))
	f.s.chunks <- "**Fixed** a.go"
	f.step("Bash", "go vet")
	waitFor(t, "second step", placeholderShowing(fake, "✏️ a.go\n⚙️ go vet\n❘"))
	if anyCallHas(fake, "editMessageText", "Fixed") {
		t.Error("silent placeholder showed answer text before the final edit")
	}
	if err := f.finish("**Fixed** a.go"); err != nil {
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

// A plain run (no Progress channel): the draft never shows the text.
func TestStreamReply_SilentPlainRunShowsHeaderOnly(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "602")
	f := startSilent(t, fake) // its structured stream is left unused
	chunks := make(chan string, 8)
	result := make(chan runner.RunResult, 1)
	done := make(chan error, 1)
	go func() {
		done <- f.b.streamReply(context.Background(), f.item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()

	chunks <- "Hello"
	// No step ever arrives: a broken silent mode would draft the chunk on
	// one of the ticks between the opening draft and the final send.
	waitFor(t, "opening draft", func() bool { return len(fake.callsTo("sendMessageDraft")) >= 1 })
	settle()
	result <- runner.RunResult{FullText: "Hello"}
	close(result)
	close(chunks)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if anyCallHas(fake, "sendMessageDraft", "Hello") {
		t.Errorf("plain-run text reached a draft: %q", lastDraftText(fake))
	}
	if !anyCallHas(fake, "sendMessage", "Hello") {
		t.Fatalf("final = %+v, want Hello", fake.callsTo("sendMessage"))
	}
	f.s.finish("unused")
	<-f.done
}

// Stop after text arrived delivers that text (D021).
func TestStreamReply_SilentStopDeliversPartialText(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "603")
	f := startSilent(t, fake)
	defer f.b.registerInflight(f.item.ID, f.item.ChatID, 0, func() {})()

	f.step("Bash", "ls")
	f.step("Bash", "cat x")
	waitFor(t, "two steps", draftShowing(fake, "⚙️ ls\n⚙️ cat x"))
	f.s.chunks <- "Half an answer"
	settle()
	if anyCallHas(fake, "sendMessageDraft", "Half") {
		t.Error("silent draft showed answer text")
	}
	f.b.bindDraft(f.item.ID, f.item.ChatID, 4343)
	f.b.handleGenerationStopped(context.Background(), stopUpdate(f.item.ChatID, 4343))
	if err := <-f.done; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	go f.s.finish("x")
	if got := fake.callsTo("sendMessage")[0].Form["text"]; !strings.Contains(got, "Half an answer") || strings.Contains(got, "⚙️") {
		t.Errorf("stopped reply = %q", got)
	}
}

// Edit path, reply longer than one message: the placeholder keeps the
// progress summary and the reply goes out separately, nothing is lost
// to a rejected edit.
func TestStreamReply_SilentEditPathLongReplySentSeparately(t *testing.T) {
	fake := strictEditPathFake()
	f := startSilent(t, fake)

	f.step("Bash", "make")
	waitFor(t, "placeholder with step", placeholderShowing(fake, "⚙️ make\n❘"))
	long := strings.Repeat("Line of the answer number one.\n", 200) + "THE END"
	f.s.chunks <- long
	if err := f.finish(long); err != nil {
		t.Fatal(err)
	}
	edits := fake.callsTo("editMessageText")
	final := edits[len(edits)-1].Form["text"]
	if !strings.Contains(final, "⚙️ make") || strings.Contains(final, "❘") || strings.Contains(final, "Line of") {
		t.Errorf("placeholder final = %q, want the progress summary without cursor", final)
	}
	got := repliesAfterPlaceholder(fake)
	if !strings.Contains(got, "THE END") || strings.Count(got, "Line of") != 200 {
		t.Errorf("reply has %d lines, end=%v; want the whole reply", strings.Count(got, "Line of"), strings.Contains(got, "THE END"))
	}
}

// Edit path, Stop with a long partial: same split.
func TestStreamReply_SilentEditPathStopLongPartial(t *testing.T) {
	fake := strictEditPathFake()
	f := startSilent(t, fake)
	defer f.b.registerInflight(f.item.ID, f.item.ChatID, 0, func() {})()

	f.step("Bash", "make")
	f.step("Bash", "make test")
	waitFor(t, "two steps", placeholderShowing(fake, "⚙️ make\n⚙️ make test\n❘"))
	f.s.chunks <- strings.Repeat("Partial line.\n", 400) + "PARTIAL END"
	settle()
	if anyCallHas(fake, "editMessageText", "Partial line") {
		t.Error("silent placeholder showed answer text")
	}
	if !f.b.stopTurn(f.item.ID, f.item.ChatID) {
		t.Fatal("stopTurn refused")
	}
	if err := <-f.done; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	go f.s.finish("x")
	if got := repliesAfterPlaceholder(fake); !strings.Contains(got, "PARTIAL END") {
		t.Errorf("stopped reply sends = %q, want the partial text", got)
	}
}
