package telegram

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestDurableStopCompleteBatchBeforeCancelAndLateResult(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "returned", true: "no_replay"}[failure], func(t *testing.T) {
			fake := &fakeTelegram{respond: okResponder}
			b, item := newStreamFixture(t, fake)
			if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(item.ChatID), Folder: item.Folder, IsOwner: true, AddedAt: 1}); err != nil {
				t.Fatal(err)
			}
			other := item
			id, _, err := b.store.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "second", ChatFolder: item.Folder, ChatID: item.ChatID, Text: "second"})
			if err != nil {
				t.Fatal(err)
			}
			other.ID = id
			for _, id := range []int64{item.ID, other.ID} {
				if err := b.store.MarkTurnRunning(t.Context(), id); err != nil {
					t.Fatal(err)
				}
			}
			item.Attempts = 1
			other.Attempts = 1
			conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(item.ChatID), 0, store.VisibilityConversation)
			if err != nil {
				t.Fatal(err)
			}
			binding := store.OrdinaryStopBinding{Conversation: conv, Members: []store.ResultMember{{TurnID: item.ID, Attempt: 1}, {TurnID: other.ID, Attempt: 1}}}
			entered, release := make(chan struct{}), make(chan struct{})
			terminal := &runner.ResultFreezeError{Err: errors.New("unknown native freeze")}
			b.syncRun = func(ctx context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
				close(entered)
				<-ctx.Done()
				if _, yes, err := b.store.FindOrdinaryStop(t.Context(), binding); err != nil || !yes {
					t.Error("native canceled before durable full batch", err)
				}
				<-release
				if failure {
					return "late private result", terminal
				}
				return "late private result", nil
			}
			done := make(chan error, 1)
			go func() { done <- b.NewAgentHandler()(t.Context(), []queue.Item{item, other}) }()
			select {
			case <-entered:
			case <-time.After(3 * time.Second):
				t.Fatal("model not entered")
			}
			if outcome := b.RequestStopCurrentTurn(t.Context(), item.ChatID, 0); outcome != control.StopAccepted {
				t.Fatal(outcome)
			}
			if outcome := b.RequestStopCurrentTurn(t.Context(), item.ChatID, 0); outcome != control.StopAlreadyRequested {
				t.Fatal(outcome)
			}
			close(release)
			select {
			case err := <-done:
				if failure {
					if !errors.Is(err, terminal) {
						t.Fatal(err)
					}
				} else if !errors.Is(err, queue.ErrStopped) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("handler hung")
			}
			stopID, yes, err := b.store.FindOrdinaryStop(t.Context(), binding)
			if err != nil || !yes {
				t.Fatal(err)
			}
			completion, err := b.store.GetStopCompletion(t.Context(), stopID)
			want := "returned"
			if failure {
				want = "no_replay"
			}
			if err != nil || completion.Text != "late private result" || completion.Outcome != want {
				t.Fatal(completion, err)
			}
			for _, method := range []string{"sendMessage", "editMessageText"} {
				for _, call := range fake.callsTo(method) {
					if call.Form["text"] == "late private result" {
						t.Fatal("late result delivered")
					}
				}
			}
			for _, it := range []queue.Item{item, other} {
				row, err := b.store.GetTurn(t.Context(), it.ID)
				if err != nil || row.ReplyMsgID != 0 {
					t.Fatal(row, err)
				}
			}
		})
	}
}

func TestDurableStopPendingFencesFinalAndFailureHolds(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "failure"}[fail], func(t *testing.T) {
			entered, release := make(chan struct{}), make(chan struct{})
			var cancels atomic.Int32
			turn := &inflightTurn{stopped: make(chan struct{}), cancel: func() { cancels.Add(1) }, persistStop: func(context.Context) (string, error) {
				close(entered)
				<-release
				if fail {
					return "", errors.New("database unavailable")
				}
				return "intent", nil
			}}
			outcome := make(chan control.StopOutcome, 1)
			go func() { outcome <- turn.requestStop(t.Context()) }()
			<-entered
			final := make(chan error, 1)
			go func() { final <- turn.beginFinal() }()
			select {
			case <-final:
				t.Fatal("final crossed pending commit")
			case <-time.After(20 * time.Millisecond):
			}
			if cancels.Load() != 0 {
				t.Fatal("cancel before commit")
			}
			close(release)
			got, err := <-outcome, <-final
			if fail {
				if got != control.StopFailed || !noReplayError(err) || cancels.Load() != 0 {
					t.Fatal(got, err, cancels.Load())
				}
			} else if got != control.StopAccepted || !errors.Is(err, queue.ErrStopped) || cancels.Load() != 1 {
				t.Fatal(got, err, cancels.Load())
			}
		})
	}
}

type ambiguousStopStore struct {
	found   bool
	readErr error
}

func (s ambiguousStopStore) RecordOrdinaryStop(context.Context, store.OrdinaryStopBinding) (string, error) {
	return "", errors.New("commit acknowledgement unknown")
}
func (s ambiguousStopStore) FindOrdinaryStop(context.Context, store.OrdinaryStopBinding) (string, bool, error) {
	return "exact", s.found, s.readErr
}
func TestDurableStopAmbiguousCommitReadback(t *testing.T) {
	for _, state := range []ambiguousStopStore{{found: true}, {}, {found: true, readErr: errors.New("read failed")}} {
		id, err := recordStopIntent(t.Context(), state, store.OrdinaryStopBinding{})
		if state.found && state.readErr == nil {
			if err != nil || id != "exact" {
				t.Fatal(id, err)
			}
		} else if err == nil {
			t.Fatal("unconfirmed Stop accepted")
		}
	}
}

func TestTerminalErrorJoinsPendingStop(t *testing.T) {
	for _, mode := range []string{"sync", "stream", "fallback"} {
		t.Run(mode, func(t *testing.T) {
			b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
			ctx, unregister := b.registerFinalGeneration(t.Context(), item, func() {})
			defer unregister()
			turn := finalGenerationTurn(ctx)
			entered, release := make(chan struct{}), make(chan struct{})
			turn.persistStop = func(context.Context) (string, error) { close(entered); <-release; return "intent", nil }
			quit, joined := make(chan struct{}), make(chan struct{})
			b.waitRefresh = map[waitKey]waitRefresher{b.waitKeyOf(t.Context(), item): {quit: quit, done: joined}}
			terminal := errors.New("native failed")
			done := make(chan error, 1)
			if mode == "sync" {
				b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) { return "", terminal }
				go func() { done <- b.runAgentSyncContext(ctx, ctx, item) }()
			} else {
				chunks := make(chan string)
				close(chunks)
				result := make(chan runner.RunResult, 1)
				result <- runner.RunResult{Err: terminal}
				stream := &runner.AgentStream{Chunks: chunks, Result: result}
				if mode == "stream" {
					go func() { done <- b.streamReply(ctx, item, stream) }()
				} else {
					go func() { done <- b.drainAndSend(ctx, item, stream) }()
				}
			}
			select {
			case <-quit:
			case <-time.After(3 * time.Second):
				t.Fatal("terminal preview join not reached")
			}
			stopped := make(chan control.StopOutcome, 1)
			go func() { stopped <- turn.requestStop(ctx) }()
			<-entered
			close(joined)
			select {
			case err := <-done:
				t.Fatal("terminal crossed pending Stop", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(release)
			if got := <-stopped; got != control.StopAccepted {
				t.Fatal(got)
			}
			select {
			case err := <-done:
				if !errors.Is(err, queue.ErrStopped) {
					t.Fatal(err)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("terminal stalled")
			}
		})
	}
}

func TestUnconfirmedStopStreamStaysAttachedUntilNativeReturns(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	native, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx, unregister := b.registerFinalGeneration(t.Context(), item, cancel)
	defer unregister()
	turn := finalGenerationTurn(ctx)
	turn.persistStop = func(context.Context) (string, error) { return "", errors.New("write failed") }
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	done := make(chan error, 1)
	go func() {
		defer cancel()
		done <- b.streamReply(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	if outcome := turn.requestStop(ctx); outcome != control.StopFailed {
		t.Fatal(outcome)
	}
	select {
	case <-native.Done():
		t.Fatal("uncommitted Stop canceled active native")
	case err := <-done:
		t.Fatal("handler detached", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(chunks)
	result <- runner.RunResult{FullText: "late"}
	select {
	case err := <-done:
		if !noReplayError(err) {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("hold did not settle returned native")
	}
}

func TestAcceptedStopJoinsPreviewAndSealsLaterRequests(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{})
	ctx, unregister := b.registerFinalGeneration(t.Context(), item, func() {})
	defer unregister()
	turn := finalGenerationTurn(ctx)
	entered, release := make(chan struct{}), make(chan struct{})
	previewDone := make(chan error, 1)
	go func() {
		previewDone <- withGenerationPreview(ctx, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	stopped := make(chan control.StopOutcome, 1)
	go func() { stopped <- turn.requestStop(ctx) }()
	select {
	case <-stopped:
		t.Fatal("Stop accepted before issued preview returned")
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-previewDone; err != nil {
		t.Fatal(err)
	}
	if got := <-stopped; got != control.StopAccepted {
		t.Fatal(got)
	}
	if err := withGenerationPreview(ctx, func() error { t.Fatal("preview after accepted Stop"); return nil }); !errors.Is(err, errPreviewSealed) {
		t.Fatal(err)
	}
}

func TestWaitPreviewHelpersCannotReopenAcceptedStop(t *testing.T) {
	fake := &fakeTelegram{respond: okResponder}
	b, item := newStreamFixture(t, fake)
	ctx, unregister := b.registerFinalGeneration(t.Context(), item, func() {})
	defer unregister()
	if finalGenerationTurn(ctx).requestStop(ctx) != control.StopAccepted {
		t.Fatal("stop failed")
	}
	b.waitPlaceholders = map[waitKey]int{b.waitKeyOf(t.Context(), item): 123}
	b.waitNotice(ctx, item)(runner.WaitSlot)
	b.takeWaitPlaceholder(ctx, item)
	b.preparePrivateStop(ctx, item)
	b.waitNotice(ctx, item)(runner.WaitMemory)
	for _, method := range []string{"sendMessage", "sendMessageDraft", "editMessageText"} {
		if calls := fake.callsTo(method); len(calls) != 0 {
			t.Fatal("preview after accepted Stop", method, calls)
		}
	}
}

func TestUnconfirmedStopHoldYieldsToIndependentShutdown(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	parent, shutdown := context.WithCancel(t.Context())
	defer shutdown()
	ctx, unregister := b.registerFinalGeneration(parent, item, func() {})
	defer unregister()
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	hold := &stopPersistenceError{errors.New("Stop unconfirmed")}
	done := make(chan error, 1)
	go func() {
		done <- b.drainHeldStream(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result}, hold)
	}()
	shutdown()
	select {
	case err := <-done:
		if !noReplayError(err) || !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown blocked on native")
	}
	close(chunks)
	result <- runner.RunResult{Err: hold}
}

func TestFailedStopCannotStartSyncFallback(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	ctx, unregister := b.registerFinalGeneration(t.Context(), item, func() {})
	defer unregister()
	turn := finalGenerationTurn(ctx)
	turn.persistStop = func(context.Context) (string, error) { return "", errors.New("commit unknown") }
	if turn.requestStop(ctx) != control.StopFailed {
		t.Fatal("unexpected acceptance")
	}
	b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) {
		t.Fatal("native fallback after unconfirmed Stop")
		return "", nil
	}
	if err := b.runAgentSyncContext(ctx, ctx, item); !noReplayError(err) {
		t.Fatal(err)
	}
}
