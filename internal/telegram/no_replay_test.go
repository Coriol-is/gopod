package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestSyncHandlerPreservesNoReplayAcrossShutdown(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			fake := &fakeTelegram{respond: okResponder}
			b, item := newStreamFixture(t, fake)
			if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(item.ChatID), Folder: item.Folder, IsOwner: true, AddedAt: 1}); err != nil {
				t.Fatal(err)
			}
			if err := b.store.MarkTurnRunning(t.Context(), item.ID); err != nil {
				t.Fatal(err)
			}
			item.Attempts = 1
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			failure := &runner.ResultFreezeError{Err: errors.New("synthetic persistence failure")}
			b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) {
				if cancelled {
					cancel()
				}
				return "", failure
			}
			b.streamEnabled = false
			err := b.NewAgentHandler()(ctx, []queue.Item{item})
			if !errors.Is(err, failure) || !noReplayError(err) {
				t.Fatalf("lost terminal error: %v", err)
			}
		})
	}
}

func TestStreamingPathsPreserveNoReplayAndOfferNoRetry(t *testing.T) {
	for _, path := range []string{"normal", "placeholder refused", "overflow placeholder refused", "drain"} {
		t.Run(path, func(t *testing.T) {
			sends := 0
			fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
				if method == "sendMessage" {
					sends++
					if path == "placeholder refused" && sends == 1 || path == "overflow placeholder refused" && sends == 2 {
						return http.StatusBadRequest, `{"ok":false,"error_code":400,"description":"synthetic refusal"}`
					}
				}
				return okResponder(method, form)
			}}
			b, item := newStreamFixture(t, fake)
			b.queue = queue.New(t.Context(), func(context.Context, []queue.Item) error { return nil }, 1, nil, nil)
			defer b.queue.Close()
			failure := &runner.ResultFreezeError{Err: errors.New("synthetic persistence failure")}
			chunks := make(chan string, 1)
			if path == "overflow placeholder refused" {
				chunks <- strings.Repeat("x", 5000)
			}
			close(chunks)
			result := make(chan runner.RunResult, 1)
			result <- runner.RunResult{Err: failure}
			stream := &runner.AgentStream{Chunks: chunks, Result: result}
			var err error
			if path == "drain" {
				err = b.drainAndSend(t.Context(), item, stream)
			} else {
				err = b.streamReply(t.Context(), item, stream)
			}
			if !errors.Is(err, failure) || !noReplayError(err) {
				t.Fatalf("lost error: %v", err)
			}
			found := false
			for _, call := range fake.callsTo("sendMessage") {
				if strings.Contains(call.Form["text"], errTextResultCommit) {
					found = true
					if call.Form["reply_markup"] != "" {
						t.Fatalf("unsafe replay action: %s", call.Form["reply_markup"])
					}
				}
			}
			if !found {
				t.Fatal("missing truthful persistence error")
			}
		})
	}
}

func TestStopPreservesAlreadyReadyNoReplay(t *testing.T) {
	for _, edit := range []bool{false, true} {
		t.Run(fmt.Sprint(edit), func(t *testing.T) {
			b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
			failure := &runner.ResultFreezeError{Err: errors.New("commit failed")}
			chunks := make(chan string)
			close(chunks)
			result := make(chan runner.RunResult, 1)
			result <- runner.RunResult{Err: failure}
			stream := &runner.AgentStream{Chunks: chunks, Result: result}
			var err error
			if edit {
				err = b.finishStoppedEdit(t.Context(), item, stream, 99, "partial", false, "")
			} else {
				err = b.finishStopped(t.Context(), item, stream, "partial")
			}
			if !errors.Is(err, failure) {
				t.Fatalf("Stop lost ready completion: %v", err)
			}
		})
	}
}

func TestCancellationProbeIsBoundedAndPreservesReadyNoReplay(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	failure := &runner.ResultFreezeError{Err: errors.New("commit failed")}
	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{Err: failure}
	settle := b.discardCancelledStream(t.Context(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	if err := settle(context.Canceled); !errors.Is(err, failure) {
		t.Fatal(err)
	}
	close(chunks)

	lateChunks := make(chan string)
	lateResult := make(chan runner.RunResult)
	settle = b.discardCancelledStream(t.Context(), item, &runner.AgentStream{Chunks: lateChunks, Result: lateResult})
	if err := settle(context.Canceled); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	close(lateChunks)
	lateResult <- runner.RunResult{Err: failure} // proves the background drain still consumes completion
}
