package telegram

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// stripHeader drops the status header line (statusline.go) from a
// draft or placeholder text, for tests about what is below it.
func stripHeader(text string) string {
	first, rest, ok := strings.Cut(text, "\n")
	if ok && (strings.HasPrefix(first, "⏳ ") || strings.HasPrefix(first, "в очереди")) {
		return rest
	}
	return text
}

func TestStatusHeader_Format(t *testing.T) {
	for _, c := range []struct {
		elapsed time.Duration
		steps   int
		pos     int
		want    string
	}{
		{42 * time.Second, 5, 0, "⏳ 0:42 · 5 шагов"},
		{0, 1, 0, "⏳ 0:00 · 1 шаг"},
		{61*time.Second + 900*time.Millisecond, 3, 0, "⏳ 1:01 · 3 шага"},
		{10 * time.Minute, 11, 0, "⏳ 10:00 · 11 шагов"},
		{time.Minute, 21, 0, "⏳ 1:00 · 21 шаг"},
		{time.Minute, 112, 0, "⏳ 1:00 · 112 шагов"},
		{3723 * time.Second, 0, 0, "⏳ 1:02:03"},
		{-time.Second, 0, 0, "⏳ 0:00"},
		{time.Hour, 9, 2, "в очереди: 2-й"},
	} {
		if got := statusHeader(c.elapsed, c.steps, c.pos); got != c.want {
			t.Errorf("statusHeader(%v, %d, %d) = %q, want %q", c.elapsed, c.steps, c.pos, got, c.want)
		}
	}
}

func TestDraftShowsHeaderAboveSteps(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "go test ./..."}
	waitFor(t, "header above the step", func() bool {
		return lastDraftText(fake) == "⏳ 0:00 · 1 шаг\n⚙️ go test ./...\n❘"
	})
	// The clock moves: the next tick shows it, with no other change.
	clock.Advance(42 * time.Second)
	waitFor(t, "elapsed time updated", func() bool {
		return lastDraftText(fake) == "⏳ 0:42 · 1 шаг\n⚙️ go test ./...\n❘"
	})
	// Nothing changes: no further drafts (compare before sending).
	n := len(fake.callsTo("sendMessageDraft"))
	time.Sleep(60 * time.Millisecond) // several ticks
	if m := len(fake.callsTo("sendMessageDraft")); m != n {
		t.Errorf("unchanged view re-sent: %d drafts, then %d", n, m)
	}
	s.finish("ok")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestHeaderDropsWhenAnswerTextStarts(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Read", Detail: "/workspace/chat/a.go"}
	waitFor(t, "header", draftShowing(fake, "📖 a.go"))
	s.chunks <- "The answer"
	waitFor(t, "answer text", func() bool { return lastDraftText(fake) == "The answer❘" })
	clock.Advance(time.Minute)
	time.Sleep(60 * time.Millisecond)
	if got := lastDraftText(fake); got != "The answer❘" {
		t.Errorf("no header once the answer streams: %q", got)
	}
	s.finish("The answer")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A plain run (no steps) shows the header only after headerQuiet, so a
// quick reply never flashes it.
func TestHeaderAfterQuietWithoutSteps(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()

	time.Sleep(50 * time.Millisecond)
	if got := lastDraftText(fake); got != "❘" {
		t.Errorf("before headerQuiet only the cursor: %q", got)
	}
	clock.Advance(headerQuiet)
	waitFor(t, "header after quiet", func() bool { return lastDraftText(fake) == "⏳ 0:05\n❘" })
	s.finish("ok")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEditPathHeaderEvery5s(t *testing.T) {
	fake := editPathFake()
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	defer b.registerInflight(item.ID, item.ChatID, func() {})()
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Edit", Detail: "/workspace/chat/a.go"}
	editShowing := func(text string) func() bool {
		return func() bool {
			for _, c := range fake.callsTo("editMessageText") {
				if c.Form["text"] == text {
					return true
				}
			}
			return false
		}
	}
	waitFor(t, "step with header", editShowing("⏳ 0:00 · 1 шаг\n✏️ a.go\n❘"))
	n := len(fake.callsTo("editMessageText"))

	// The clock alone: no edit before 5 s.
	clock.Advance(4 * time.Second)
	time.Sleep(60 * time.Millisecond)
	if m := len(fake.callsTo("editMessageText")); m != n {
		t.Fatalf("header-only edit before 5 s: %d edits, then %d", n, m)
	}
	clock.Advance(time.Second)
	if !waitForOK(editShowing("⏳ 0:05 · 1 шаг\n✏️ a.go\n❘")) {
		for _, c := range fake.callsTo("editMessageText") {
			t.Logf("edit %q", c.Form["text"])
		}
		t.Fatal("timed out waiting for header edit at 5 s")
	}
	time.Sleep(60 * time.Millisecond)
	edits := fake.callsTo("editMessageText")
	if len(edits) != n+1 {
		t.Fatalf("one header edit per 5 s: %d edits after %d", len(edits), n)
	}
	if !hasStop(t, edits[len(edits)-1], item.ID) {
		t.Error("a header edit must keep the Stop button")
	}
	s.finish("done")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestEditPathHeaderRespects429(t *testing.T) {
	fake := editPathFake()
	var mu sync.Mutex
	limited := false
	base := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		mu.Lock()
		defer mu.Unlock()
		if method == "editMessageText" && limited {
			return apiError(http.StatusTooManyRequests, "Too Many Requests: retry after 30")
		}
		return base(method, form)
	}
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()

	s.progress <- runner.ProgressEvent{Tool: "Edit", Detail: "/workspace/chat/a.go"}
	waitFor(t, "first edit", func() bool { return len(fake.callsTo("editMessageText")) > 0 })
	mu.Lock()
	limited = true
	mu.Unlock()
	clock.Advance(5 * time.Second)
	waitFor(t, "limited edit", func() bool { return len(fake.callsTo("editMessageText")) == 2 })
	clock.Advance(5 * time.Second)
	s.progress <- runner.ProgressEvent{Tool: "Edit", Detail: "/workspace/chat/b.go"}
	time.Sleep(80 * time.Millisecond)
	if n := len(fake.callsTo("editMessageText")); n != 2 {
		t.Errorf("no edits during retry_after: %d", n)
	}
	mu.Lock()
	limited = false
	mu.Unlock()
	s.finish("done")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// A turn waiting behind another conversation of the chat shows its
// queue position in a draft of its own thread, never in the thread
// that is streaming, and stops before its turn starts.
func TestQueueWaitDraftShowsPosition(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b := newFakeBot(t, fake)
	b.streamEnabled = true
	b.streamTick = 10 * time.Millisecond
	release := make(chan struct{})
	q := queue.New(t.Context(), func(ctx context.Context, items []queue.Item) error {
		<-release
		return nil
	}, 10, nil, b.log)
	defer q.Close()
	defer close(release)
	b.queue = q
	ctx := t.Context()

	running := queue.Item{ID: 1, ChatID: streamChat, ThreadID: 1, Folder: "owner"}
	q.Enqueue(ctx, running)
	waitFor(t, "first turn running", func() bool { return q.Position("owner", 1) == 0 && q.ActiveChats() == 1 })
	defer b.markThreadBusy(running)()

	waiting := queue.Item{ID: 2, ChatID: streamChat, ThreadID: 2, Folder: "owner"}
	sameThread := queue.Item{ID: 3, ChatID: streamChat, ThreadID: 1, Folder: "owner"}
	q.Enqueue(ctx, waiting)
	q.Enqueue(ctx, sameThread)
	b.startQueueWait(ctx, waiting)
	b.startQueueWait(ctx, sameThread)

	waitFor(t, "queue draft", func() bool { return lastDraftText(fake) == "в очереди: 1-й" })
	time.Sleep(60 * time.Millisecond) // several ticks
	drafts := fake.callsTo("sendMessageDraft")
	for _, d := range drafts {
		if d.Form["message_thread_id"] != "2" {
			t.Errorf("queue draft outside the waiting turn's thread: %v", d.Form)
		}
	}
	if len(drafts) != 1 {
		t.Errorf("an unchanged position is not re-sent: %d drafts", len(drafts))
	}
	b.stopQueueWaits(streamChat, 2)
	b.stopQueueWaits(streamChat, 1)
	n := len(fake.callsTo("sendMessageDraft"))
	time.Sleep(50 * time.Millisecond)
	if m := len(fake.callsTo("sendMessageDraft")); m != n {
		t.Errorf("queue draft after stopQueueWaits: %d then %d", n, m)
	}
}

func waitForOK(cond func() bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(5 * time.Millisecond)
	}
	return false
}

// blockingQueue is a queue whose handler holds every batch until the
// test ends.
func blockingQueue(t *testing.T, b *Bot, maxConc int) *queue.Queue {
	t.Helper()
	release := make(chan struct{})
	q := queue.New(t.Context(), func(ctx context.Context, items []queue.Item) error {
		select {
		case <-release:
		case <-ctx.Done():
		}
		return nil
	}, maxConc, nil, b.log)
	t.Cleanup(func() { close(release); q.Close() })
	return q
}

// An ordinary turn in an idle chat never shows a queue position: no
// draft, and nothing for the starting turn to wait on.
func TestQueueWait_IdleChatNoDraft(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b := newFakeBot(t, fake)
	b.streamEnabled = true
	b.streamTick = 10 * time.Millisecond
	b.queue = blockingQueue(t, b, 10)
	item := queue.Item{ID: 1, ChatID: streamChat, ThreadID: 2, Folder: "owner"}
	b.queue.Enqueue(t.Context(), item)
	b.startQueueWait(t.Context(), item) // Position may still say 1 here
	start := time.Now()
	b.stopQueueWaits(streamChat, 2)
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Errorf("turn start waited %v for a queue draft", d)
	}
	time.Sleep(60 * time.Millisecond)
	if n := len(fake.callsTo("sendMessageDraft")); n != 0 {
		t.Errorf("idle chat: %d queue drafts", n)
	}
}

// All agent slots taken by another chat: the turn waits, but not behind
// a conversation of its own chat, so no position is shown.
func TestQueueWait_SemaphoreFullNoDraft(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b := newFakeBot(t, fake)
	b.streamEnabled = true
	b.streamTick = 10 * time.Millisecond
	b.queue = blockingQueue(t, b, 1)
	b.queue.Enqueue(t.Context(), queue.Item{ID: 1, ChatID: 42, Folder: "other"})
	waitFor(t, "other chat running", func() bool { return b.queue.Position("other", 1) == 0 })
	item := queue.Item{ID: 2, ChatID: streamChat, ThreadID: 2, Folder: "owner"}
	b.queue.Enqueue(t.Context(), item)
	if p := b.queue.Position("owner", 2); p != 1 {
		t.Fatalf("position = %d, want 1 (waiting for a slot)", p)
	}
	b.startQueueWait(t.Context(), item)
	time.Sleep(60 * time.Millisecond)
	if n := len(fake.callsTo("sendMessageDraft")); n != 0 {
		t.Errorf("semaphore full: %d queue drafts", n)
	}
}

// A queue draft stuck on the network does not hold up the turn start.
func TestQueueWait_StopIsBounded(t *testing.T) {
	fake := &fakeTelegram{}
	hang := make(chan struct{})
	defer close(hang)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessageDraft" {
			<-hang
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b := newFakeBot(t, fake)
	b.streamEnabled = true
	b.streamTick = 10 * time.Millisecond
	b.queue = blockingQueue(t, b, 10)
	running := queue.Item{ID: 1, ChatID: streamChat, ThreadID: 1, Folder: "owner"}
	b.queue.Enqueue(t.Context(), running)
	waitFor(t, "running", func() bool { return b.queue.Position("owner", 1) == 0 })
	defer b.markThreadBusy(running)()
	item := queue.Item{ID: 2, ChatID: streamChat, ThreadID: 2, Folder: "owner"}
	b.queue.Enqueue(t.Context(), item)
	b.startQueueWait(t.Context(), item)
	waitFor(t, "draft in flight", func() bool { return len(fake.callsTo("sendMessageDraft")) == 1 })
	start := time.Now()
	b.stopQueueWaits(streamChat, 2)
	if d := time.Since(start); d > time.Second {
		t.Errorf("stopQueueWaits waited %v on a hung draft", d)
	}
}

// A 60 s tool-only turn: the clock alone changes the draft every
// second, but drafts go out at most every 3 s, well under the per-peer
// 40 per 30 s shared with the typing action (every 4 s).
func TestDraftHeaderBudget(t *testing.T) {
	fake := &fakeTelegram{}
	okDrafts(fake, "555")
	b, item := newStreamFixture(t, fake)
	clock := newManualClock()
	b.reactClock = clock
	s := newStructuredStream()
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, s.stream()) }()
	s.progress <- runner.ProgressEvent{Tool: "Bash", Detail: "sleep 60"}
	waitFor(t, "step", draftShowing(fake, "⚙️ sleep 60"))
	before := len(fake.callsTo("sendMessageDraft"))
	for i := 0; i < 60; i++ {
		clock.Advance(time.Second)
		time.Sleep(15 * time.Millisecond) // at least one tick
	}
	n := len(fake.callsTo("sendMessageDraft")) - before
	if n > 21 || n < 15 {
		t.Errorf("clock-only drafts in 60 s = %d, want about 20", n)
	}
	s.finish("ok")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
