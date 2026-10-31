package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// structuredStream is a fake structured run: answer chunks (buffered,
// like the runner's) and unbuffered progress.
type structuredStream struct {
	chunks   chan string
	progress chan runner.ProgressEvent
	result   chan runner.RunResult
}

func newStructuredStream() *structuredStream {
	return &structuredStream{
		chunks:   make(chan string, 32),
		progress: make(chan runner.ProgressEvent),
		result:   make(chan runner.RunResult, 1),
	}
}

func (s *structuredStream) stream() *runner.AgentStream {
	return &runner.AgentStream{Chunks: s.chunks, Progress: s.progress, Result: s.result}
}

// finish ends the run the way the runner does.
func (s *structuredStream) finish(text string) {
	close(s.progress)
	s.result <- runner.RunResult{FullText: text}
	close(s.result)
	close(s.chunks)
}

func okDrafts(fake *fakeTelegram, sendID string) {
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage(sendID)
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
}

func lastDraftText(fake *fakeTelegram) string {
	d := fake.callsTo("sendMessageDraft")
	if len(d) == 0 {
		return ""
	}
	return d[len(d)-1].Form["text"]
}

func TestStreamReply_DraftShowsProgressThenAnswer(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "go test ./..."}
	waitFor(t, "first step", draftShowing(fake, "⚙️ go test ./...\n❘"))
	s.progress <- runner.ProgressEvent{Tool: "Read", Detail: "/workspace/chat/main.go"}
	waitFor(t, "two steps", draftShowing(fake, "⚙️ go test ./...\n📖 main.go\n❘"))

	s.chunks <- "All tests "
	s.chunks <- "pass."
	waitFor(t, "answer text", draftShowing(fake, "All tests pass."))
	if got := lastDraftText(fake); strings.Contains(got, "⚙️") {
		t.Errorf("answer draft still shows steps: %q", got)
	}
	s.finish("All tests pass.")
	if err := <-done; err != nil {
		t.Fatalf("streamReply: %v", err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !strings.Contains(sends[0].Form["text"], "All tests pass.") ||
		strings.Contains(sends[0].Form["text"], "go test") {
		t.Fatalf("final = %+v, want the answer only", sends)
	}
	for _, d := range fake.callsTo("sendMessageDraft") {
		if d.Form["parse_mode"] != "" {
			t.Errorf("draft with parse_mode %q: progress lines are plain text", d.Form["parse_mode"])
		}
	}
}

// Only the last progressShown steps are shown; a step after answer
// text (narration) brings the steps back and drops the narration.
func TestStreamReply_ProgressKeepsLastFiveAndNarrationYields(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "556")
	b, item := newStreamFixture(t, fake)
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

	for i := 1; i <= 7; i++ {
		s.progress <- runner.ProgressEvent{Tool: "Grep", Detail: "p" + strconv.Itoa(i)}
	}
	want := "🔎 p3\n🔎 p4\n🔎 p5\n🔎 p6\n🔎 p7\n❘"
	waitFor(t, "last five steps", draftShowing(fake, want))

	// Narration, then a step: the runner sends the chunk first; it sits
	// in the buffer while the step is handed over.
	s.chunks <- "Let me check the config."
	s.progress <- runner.ProgressEvent{Tool: "Read", Detail: "/workspace/chat/cfg.yaml"}
	waitFor(t, "step after narration", draftShowing(fake, "🔎 p4\n🔎 p5\n🔎 p6\n🔎 p7\n📖 cfg.yaml\n❘"))
	time.Sleep(50 * time.Millisecond) // several ticks
	if got := lastDraftText(fake); strings.Contains(got, "Let me check") {
		t.Errorf("narration shown after a later step: %q", got)
	}
	s.chunks <- "Done."
	s.finish("Done.")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// Edit path (drafts refused): the placeholder shows the steps and the
// final edit replaces them with the answer.
func TestStreamReply_EditPathShowsProgressInPlaceholder(t *testing.T) {
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

// Stop while only steps are shown: the reply is the stopped note (no
// answer text exists), and the runner, still sending steps and text,
// never blocks.
func TestStreamReply_StopDuringProgress(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "778")
	b, item := newStreamFixture(t, fake)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	defer b.registerInflight(item.ID, item.ChatID, 0, cancelRun)()
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.chunks <- "Narration before a tool."
	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "make build"}
	waitFor(t, "step shown", draftShowing(fake, "⚙️ make build"))

	var draftID int
	for _, c := range fake.callsTo("sendMessageDraft")[len(fake.callsTo("sendMessageDraft"))-1].Form["draft_id"] {
		draftID = draftID*10 + int(c-'0')
	}
	b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, draftID))
	if runCtx.Err() == nil {
		t.Fatal("stop did not cancel the run")
	}
	waitFor(t, "stopped reply", func() bool { return len(fake.callsTo("sendMessage")) == 1 })

	// The dying run still emits; both channels must be drained.
	sent := make(chan struct{})
	go func() {
		s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "late"}
		s.chunks <- "late text"
		s.finish("late")
		close(sent)
	}()
	select {
	case <-sent:
	case <-time.After(2 * time.Second):
		t.Fatal("runner blocked: progress not drained after stop")
	}
	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v, want ErrStopped", err)
	}
	if got := fake.callsTo("sendMessage")[0].Form["text"]; got != stoppedEmpty {
		t.Errorf("stopped reply = %q, want %q (no answer text after the step)", got, stoppedEmpty)
	}
}

// Stop after answer text started: that text is the reply.
func TestStreamReply_StopAfterAnswerTextDeliversIt(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "779")
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "ls"}
	s.chunks <- "Half an answer"
	waitFor(t, "answer draft", draftShowing(fake, "Half an answer"))
	b.bindDraft(item.ID, item.ChatID, 4242)
	b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, 4242))
	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("err = %v", err)
	}
	go s.finish("x")
	if got := fake.callsTo("sendMessage")[0].Form["text"]; !strings.Contains(got, "Half an answer") || strings.Contains(got, "⚙️") {
		t.Errorf("stopped reply = %q", got)
	}
}

// Progress updates obey the draft 429 hold like text does.
func TestStreamReply_ProgressRespects429Hold(t *testing.T) {
	var drafts int32
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		switch method {
		case "sendMessage":
			return sentMessage("999")
		case "sendMessageDraft":
			if atomic.AddInt32(&drafts, 1) >= 2 { // opening draft ok, then 429
				return tooManyRequests("30")
			}
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake) // 10ms tick
	recordSleeps(b)
	s := newStructuredStream()
	errc := make(chan error, 1)
	go func() { errc <- b.streamReply(context.Background(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "a"}
	waitFor(t, "a draft hitting 429", func() bool { return atomic.LoadInt32(&drafts) >= 2 })
	for i := 0; i < 20; i++ {
		s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "b" + strconv.Itoa(i)}
		time.Sleep(10 * time.Millisecond)
	}
	s.finish("ok")
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&drafts); got != 2 {
		t.Errorf("draft calls = %d, want 2 (no draft until retry_after passes)", got)
	}
}

// A chunk and a step queued while the loop is busy (a draft call in
// flight): the chunk the runner sent first must be applied first, so
// narration never shows after the step that follows it. Without the
// drain the select picks either one, so this repeats.
func TestStreamReply_BufferedChunkAppliedBeforeStep(t *testing.T) {
	for i := 0; i < 12; i++ {
		fake := &fakeTelegram{}
		gate := make(chan struct{})
		var blocking atomic.Bool
		var draftCalls atomic.Int32
		fake.respond = func(method string, _ map[string]string) (int, string) {
			if method == "sendMessageDraft" && draftCalls.Add(1) == 2 {
				blocking.Store(true)
				<-gate // the second draft (the first step) stalls the loop
			}
			if method == "sendMessage" {
				return sentMessage("1")
			}
			return http.StatusOK, `{"ok":true,"result":true}`
		}
		b, item := newStreamFixture(t, fake)
		s := newStructuredStream()
		done := make(chan error, 1)
		go func() { done <- b.streamReply(context.Background(), item, s.stream()) }()

		s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "one"}
		waitFor(t, "loop stalled in a draft call", blocking.Load)
		s.chunks <- "Narration."
		stepSent := make(chan struct{})
		go func() {
			s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "two"}
			close(stepSent)
		}()
		time.Sleep(10 * time.Millisecond) // both queued
		close(gate)
		<-stepSent
		waitFor(t, "second step shown", draftShowing(fake, "⚙️ one\n⚙️ two\n❘"))
		time.Sleep(40 * time.Millisecond) // several ticks
		if got := lastDraftText(fake); strings.Contains(got, "Narration") {
			t.Fatalf("iteration %d: narration shown after the next step: %q", i, got)
		}
		s.finish("x")
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
}
