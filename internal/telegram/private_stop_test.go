package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

func TestStopCurrentTurnScopesChatAndTopic(t *testing.T) {
	b, _ := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	a, cancelA := context.WithCancel(t.Context())
	defer cancelA()
	otherTopic, cancelB := context.WithCancel(t.Context())
	defer cancelB()
	otherChat, cancelC := context.WithCancel(t.Context())
	defer cancelC()
	unregister := b.registerInflight(1, streamChat, 7, cancelA)
	defer unregister()
	defer b.registerInflight(2, streamChat, 8, cancelB)()
	defer b.registerInflight(3, streamChat+1, 7, cancelC)()
	if b.StopCurrentTurn(streamChat, 0) {
		t.Fatal("main chat stopped another topic")
	}
	if !b.StopCurrentTurn(streamChat, 7) || a.Err() == nil {
		t.Fatal("matching turn was not cancelled")
	}
	if otherTopic.Err() != nil || otherChat.Err() != nil {
		t.Fatal("stop escaped its conversation")
	}
	if b.StopCurrentTurn(streamChat, 7) {
		t.Fatal("duplicate stop acted twice")
	}
	unregister()
	if b.StopCurrentTurn(streamChat, 7) {
		t.Fatal("stale stop acted")
	}
}

func TestPrivateStopPlaceholderSurvivesWaitAndStreaming(t *testing.T) {
	for _, stop := range []bool{false, true} {
		t.Run(map[bool]string{false: "complete", true: "stop"}[stop], func(t *testing.T) {
			fake := &fakeTelegram{respond: okResponder}
			b, item := newStreamFixture(t, fake)
			item.ThreadID = 7
			runCtx, cancel := context.WithCancel(t.Context())
			defer cancel()
			defer b.registerInflight(item.ID, item.ChatID, item.ThreadID, cancel)()
			b.preparePrivateStop(t.Context(), item)
			sends := fake.callsTo("sendMessage")
			if len(sends) != 1 || !hasStop(t, sends[0], item.ID) || sends[0].Form["message_thread_id"] != "7" {
				t.Fatal(sends)
			}
			b.waitNotice(t.Context(), item)(runner.WaitMemory)
			if !hasStop(t, lastCall(t, fake, "editMessageText"), item.ID) {
				t.Fatal("wait removed Stop")
			}
			chunks := make(chan string, 1)
			chunks <- "partial"
			result := make(chan runner.RunResult, 1)
			done := make(chan error, 1)
			go func() { done <- b.streamReply(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result}) }()
			waitFor(t, "partial edit", func() bool {
				for _, c := range fake.callsTo("editMessageText") {
					if strings.Contains(c.Form["text"], "partial") {
						return true
					}
				}
				return false
			})
			if stop {
				b.registerCallbacks()
				b.callbackHandler(t.Context(), b.api, cbUpdate("stop", item.ChatID, item.ThreadID, 88, testStopData(b, item.ID, 88)))
				if runCtx.Err() == nil {
					t.Fatal("inline Stop did not cancel")
				}
			}
			close(chunks)
			result <- runner.RunResult{FullText: "complete"}
			select {
			case err := <-done:
				if stop && !errors.Is(err, queue.ErrStopped) || !stop && err != nil {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("turn did not end")
			}
			if len(fake.callsTo("sendMessageDraft")) != 0 || len(fake.callsTo("sendMessage")) != 1 {
				t.Fatal("private response did not reuse its placeholder")
			}
			if hasStop(t, lastCall(t, fake, "editMessageText"), item.ID) {
				t.Fatal("terminal reply kept Stop")
			}
		})
	}
}

func TestPrivateStopCommandCancelsBufferedAnswer(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	item.ThreadID = 7
	b.router = control.New(nil)
	control.RegisterStopCommand(b.router, b.StopCurrentTurn)
	entered := make(chan struct{})
	b.syncRun = func(ctx context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
		close(entered)
		<-ctx.Done()
		return "", ctx.Err()
	}
	done := make(chan error, 1)
	go func() { done <- b.runAgentSync(t.Context(), item) }()
	<-entered
	// Wrong topic is harmless even though this chat has a running answer.
	b.dispatchSlash(t.Context(), threadedMsg(8, "/stop"), "/stop")
	select {
	case err := <-done:
		t.Fatalf("wrong-topic stop ended run: %v", err)
	default:
	}
	b.dispatchSlash(t.Context(), threadedMsg(7, "/stop"), "/stop")
	select {
	case err := <-done:
		if !errors.Is(err, queue.ErrStopped) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("/stop was queued behind the answer")
	}
	if b.inflightStop(item.ID) != nil {
		t.Fatal("finished turn stayed registered")
	}
	if c := lastCall(t, fake, "editMessageText"); c.Form["text"] != stoppedEmpty || !cleared(t, c) {
		t.Fatal(c)
	}
}

func TestBufferedFallbackUsesRegisteredCancellation(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	runCtx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer b.registerInflight(item.ID, item.ChatID, item.ThreadID, cancel)()
	b.preparePrivateStop(t.Context(), item)
	b.syncRun = func(ctx context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
		if ctx != runCtx {
			t.Error("fallback lost child context")
		}
		b.StopCurrentTurn(item.ChatID, item.ThreadID)
		return "late text must not become a completed reply", nil
	}
	if err := b.runAgentSyncContext(t.Context(), runCtx, item); !errors.Is(err, queue.ErrStopped) {
		t.Fatal(err)
	}
	if c := lastCall(t, fake, "editMessageText"); c.Form["text"] != stoppedEmpty {
		t.Fatal(c)
	}
}

func TestStopMenuPrivateOnly(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, _ := newStreamFixture(t, fake)
	b.router = control.New(nil)
	control.RegisterBuiltins(b.router)
	control.RegisterStopCommand(b.router, b.StopCurrentTurn)
	b.publishCommands(t.Context())
	calls := fake.callsTo("setMyCommands")
	if len(calls) != 2 || strings.Contains(calls[0].Form["commands"], `"stop"`) || !strings.Contains(calls[1].Form["commands"], `"stop"`) {
		t.Fatal(calls)
	}
}

func TestPrivateStopBufferedVoiceOnlyDoesNotPublishAnswerText(t *testing.T) {
	voiceOnly(t)
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) {
		return "Voice-only answer content", nil
	}
	if err := b.runAgentSync(t.Context(), item); err != nil {
		t.Fatal(err)
	}
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || !hasStop(t, sends[0], item.ID) {
		t.Fatal("expected only the initial Stop placeholder", sends)
	}
	if len(fake.callsTo("deleteMessage")) != 1 || b.waitPlaceholder(item) != 0 {
		t.Fatal("voice-only completion did not retire the placeholder")
	}
	for _, c := range fake.callsTo("editMessageText") {
		if strings.Contains(c.Form["text"], "Voice-only answer content") {
			t.Fatal("voice-only answer was published as text")
		}
	}
}
