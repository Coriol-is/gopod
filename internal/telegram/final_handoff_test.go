package telegram

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

func TestFinalHandoffAndStopHaveOneWinner(t *testing.T) {
	for range 1000 {
		var canceled atomic.Int32
		turn := &inflightTurn{stopped: make(chan struct{}), cancel: func() { canceled.Add(1) }}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)
		var stopped bool
		var finalErr error
		go func() { defer wg.Done(); <-start; stopped = turn.stop() }()
		go func() { defer wg.Done(); <-start; finalErr = turn.beginFinal() }()
		close(start)
		wg.Wait()
		if stopped {
			if !errors.Is(finalErr, queue.ErrStopped) || canceled.Load() != 1 {
				t.Fatal(stopped, finalErr, canceled.Load())
			}
		} else if finalErr != nil || canceled.Load() != 0 {
			t.Fatal(stopped, finalErr, canceled.Load())
		}
		if turn.stop() {
			t.Fatal("second Stop accepted")
		}
	}
}
func TestFinalHandoffRetiredGenerationCannotOwnSuccessor(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	var first, second atomic.Int32
	unregister := b.registerInflight(1, streamChat, 0, func() { first.Add(1) })
	_, handoff, _ := b.finalHandoff(t.Context(), 1)
	b.inflightMu.Lock()
	old := b.inflight[1]
	b.inflightMu.Unlock()
	defer b.registerInflight(1, streamChat, 0, func() { second.Add(1) })()
	unregister()
	if err := handoff(t.Context()); !noReplayError(err) {
		t.Fatal(err)
	}
	if old.stop() || first.Load() != 0 {
		t.Fatal("captured retired Stop accepted")
	}
	if !b.StopCurrentTurn(streamChat, 0) || second.Load() != 1 {
		t.Fatal("old handoff changed successor")
	}
}
func TestFinalHandoffCanceledContextDoesNotAcquireOwnership(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	defer b.registerInflight(1, streamChat, 0, func() {})()
	_, handoff, _ := b.finalHandoff(t.Context(), 1)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := handoff(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if !b.stopTurn(1, streamChat) {
		t.Fatal("canceled caller acquired final ownership")
	}
}

func TestLateStopRejectedDuringFinalHTTP(t *testing.T) {
	for _, mode := range []string{"draft", "edit", "fallback", "sync"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
				if mode == "fallback" && method == "sendMessage" && form["text"] != "answer" {
					return apiError(400, "Bad Request: invalid placeholder")
				}
				final := method == "sendMessage" && form["text"] == "answer" || method == "editMessageText" && form["text"] == "answer"
				if final {
					once.Do(func() { close(entered) })
					<-release
				}
				return sentMessage("555")
			}}
			b, item := newStreamFixture(t, fake)
			if mode == "edit" || mode == "fallback" {
				b.markDraftsUnsupported(item.ChatID)
			}
			var canceled atomic.Int32
			defer b.registerInflight(item.ID, item.ChatID, item.ThreadID, func() { canceled.Add(1) })()
			done := make(chan error, 1)
			if mode == "sync" {
				b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) { return "answer", nil }
				go func() { done <- b.runAgentSyncContext(t.Context(), t.Context(), item) }()
			} else {
				chunks := make(chan string, 1)
				chunks <- "answer"
				close(chunks)
				result := make(chan runner.RunResult, 1)
				result <- runner.RunResult{FullText: "answer"} // intentionally not closed
				go func() { done <- b.streamReply(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result}) }()
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("final HTTP not reached")
			}
			if b.stopTurn(item.ID, item.ChatID) || b.StopCurrentTurn(item.ChatID, item.ThreadID) {
				close(release)
				t.Fatal("late Stop claimed success during final HTTP")
			}
			b.inflightMu.Lock()
			draft := b.inflight[item.ID].draftID
			b.inflightMu.Unlock()
			if draft != 0 {
				b.handleGenerationStopped(t.Context(), stopUpdate(item.ChatID, draft))
			}
			if canceled.Load() != 0 {
				close(release)
				t.Fatal("final HTTP canceled native generation")
			}
			close(release)
			select {
			case err := <-done:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("final handler hung")
			}
		})
	}
}

func TestSettledResultPreservesNoReplayAfterLateStop(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{})
	stream := settledResultStream(runner.RunResult{Err: &runner.ResultFreezeError{Err: errors.New("commit unknown")}})
	if err := b.discardCancelledStream(t.Context(), item, stream)(queue.ErrStopped); !noReplayError(err) {
		t.Fatal(err)
	}
}

func TestFinalHandoffDelegationNeverCapturesReplacement(t *testing.T) {
	for _, mode := range []string{"sync_to_stream", "stream_to_fallback"} {
		t.Run(mode, func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var once sync.Once
			fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
				if mode == "stream_to_fallback" && method == "sendMessage" && form["text"] != "old answer" {
					once.Do(func() { close(entered) })
					<-release
					return apiError(400, "Bad Request: placeholder refused")
				}
				return sentMessage("123")
			}}
			b, item := newStreamFixture(t, fake)
			oldDone := b.registerInflight(item.ID, item.ChatID, 0, func() {})
			defer oldDone()
			done := make(chan error, 1)
			if mode == "sync_to_stream" {
				b.waitPlaceholders = map[waitKey]int{b.waitKeyOf(t.Context(), item): 123}
				b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) {
					close(entered)
					<-release
					return "old answer", nil
				}
				go func() { done <- b.runAgentSyncContext(t.Context(), t.Context(), item) }()
			} else {
				b.markDraftsUnsupported(item.ChatID)
				chunks := make(chan string)
				close(chunks)
				result := make(chan runner.RunResult, 1)
				result <- runner.RunResult{FullText: "old answer"}
				go func() { done <- b.streamReply(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result}) }()
			}
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				close(release)
				t.Fatal("blocked stage not reached")
			}
			var canceled atomic.Int32
			defer b.registerInflight(item.ID, item.ChatID, 0, func() { canceled.Add(1) })()
			close(release)
			select {
			case err := <-done:
				if !noReplayError(err) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stale handler hung")
			}
			if !b.StopCurrentTurn(item.ChatID, 0) || canceled.Load() != 1 {
				t.Fatal("stale handler acquired replacement")
			}
			for _, method := range []string{"sendMessage", "editMessageText"} {
				for _, c := range fake.callsTo(method) {
					if c.Form["text"] == "old answer" {
						t.Fatal("stale output sent")
					}
				}
			}
		})
	}
}

func TestStopWinsAfterResultBeforePreviewJoin(t *testing.T) {
	fake := codeFake(false)
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()
	quit, joined := make(chan struct{}), make(chan struct{})
	b.waitRefresh = map[waitKey]waitRefresher{b.waitKeyOf(t.Context(), item): {quit: quit, done: joined}}
	chunks := make(chan string, 1)
	chunks <- "visible partial"
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{FullText: "full result"}
	done := make(chan error, 1)
	go func() { done <- b.streamReply(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result}) }()
	select {
	case <-quit:
	case <-time.After(3 * time.Second):
		close(joined)
		t.Fatal("preview not joined before final ownership")
	}
	if !b.stopTurn(item.ID, item.ChatID) {
		close(joined)
		t.Fatal("Stop lost before ownership transfer")
	}
	close(joined)
	select {
	case err := <-done:
		if !errors.Is(err, queue.ErrStopped) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("consumed Result was read again")
	}
	for _, call := range fake.callsTo("sendMessage") {
		if call.Form["text"] == "full result" {
			t.Fatal("normal final sent after Stop")
		}
	}
}

func TestLateStopRejectedBeforeVoiceOnlySynthesis(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "fake")
	SetReplyMode(streamChat, "voice")
	t.Cleanup(func() { SetReplyMode(streamChat, "") })
	b, item := newStreamFixture(t, voiceFake(false))
	var canceled atomic.Int32
	defer b.registerInflight(item.ID, item.ChatID, 0, func() { canceled.Add(1) })()
	b.waitPlaceholders = map[waitKey]int{b.waitKeyOf(t.Context(), item): 123}
	b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) { return "answer", nil }
	entered, release := make(chan struct{}), make(chan struct{})
	b.tts = func(context.Context, string, string) ([]byte, error) {
		close(entered)
		<-release
		return []byte("OggS-audio"), nil
	}
	done := make(chan error, 1)
	go func() { done <- b.runAgentSyncContext(t.Context(), t.Context(), item) }()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		close(release)
		t.Fatal("voice not reached")
	}
	if b.StopCurrentTurn(item.ChatID, 0) || canceled.Load() != 0 {
		close(release)
		t.Fatal("Stop accepted during voice preparation")
	}
	close(release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("voice final hung")
	}
}

func TestFinalRegistrationKeepsExactGeneration(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{})
	oldCtx, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	// A new invocation must overwrite even a same-ID inherited context binding.
	newCtx, newDone := b.registerFinalGeneration(oldCtx, item, func() {})
	defer newDone()
	_, oldHandoff, _ := b.finalHandoff(oldCtx, item.ID)
	if err := oldHandoff(oldCtx); !noReplayError(err) {
		t.Fatalf("old registration acquired replacement: %v", err)
	}
	_, newHandoff, _ := b.finalHandoff(newCtx, item.ID)
	if err := newHandoff(newCtx); err != nil {
		t.Fatalf("new registration inherited stale token: %v", err)
	}
}
