package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// stopUpdate is the update Telegram sends when the user presses Stop
// on draft draftID in chatID.
func stopUpdate(chatID int64, draftID int) *models.Update {
	return &models.Update{StoppedMessageGeneration: &models.MessageGenerationStopped{
		Chat:    models.Chat{ID: chatID, Type: models.ChatTypePrivate},
		DraftID: draftID,
	}}
}

func TestStopCancelsOnlyMatchingTurn(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	ctxA, cancelA := context.WithCancel(context.Background())
	defer cancelA()
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()

	defer b.registerInflight(1, streamChat, 0, cancelA)()
	defer b.registerInflight(2, streamChat, 0, cancelB)()
	b.bindDraft(1, streamChat, 7)
	b.bindDraft(2, streamChat, 8)

	// Same draft id in another chat matches nothing.
	b.handleGenerationStopped(context.Background(), stopUpdate(streamChat+1, 7))
	if ctxA.Err() != nil || ctxB.Err() != nil {
		t.Fatal("stop in another chat cancelled a turn")
	}

	b.handleGenerationStopped(context.Background(), stopUpdate(streamChat, 7))
	if ctxA.Err() == nil {
		t.Error("turn 1 (draft 7) not cancelled")
	}
	if ctxB.Err() != nil {
		t.Error("turn 2 (draft 8) cancelled by a stop for draft 7")
	}
}

func TestStopAfterFinishIsNoop(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	unregister := b.registerInflight(1, streamChat, 0, cancel)
	b.bindDraft(1, streamChat, 7)
	unregister()

	b.handleGenerationStopped(context.Background(), stopUpdate(streamChat, 7))
	if ctx.Err() != nil {
		t.Error("stop after the turn finished cancelled its context")
	}
	if b.inflightStop(1) != nil {
		t.Error("finished turn still registered")
	}
	// Unknown turn, nil payload: no panic.
	b.bindDraft(99, streamChat, 9)
	b.handleGenerationStopped(context.Background(), &models.Update{})
	b.handleGenerationStopped(context.Background(), nil)
}

func TestStopTwiceIsNoop(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	var cancels int32
	defer b.registerInflight(1, streamChat, 0, func() { atomic.AddInt32(&cancels, 1) })()
	b.bindDraft(1, streamChat, 7)

	b.handleGenerationStopped(context.Background(), stopUpdate(streamChat, 7))
	b.handleGenerationStopped(context.Background(), stopUpdate(streamChat, 7))
	if got := atomic.LoadInt32(&cancels); got != 1 {
		t.Errorf("cancel called %d times, want 1", got)
	}
	select {
	case <-b.inflightStop(1):
	default:
		t.Error("stop channel not closed")
	}
}

func TestDefaultHandlerRoutesStopUpdate(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer b.registerInflight(1, streamChat, 0, cancel)()
	b.bindDraft(1, streamChat, 7)

	b.defaultHandler(context.Background(), nil, stopUpdate(streamChat, 7))
	if ctx.Err() == nil {
		t.Error("defaultHandler did not route stopped_message_generation")
	}
}

// TestStreamReply_StopDeliversPartial: Stop on a streaming draft ends
// the turn with the text so far as the real reply, recorded on the row.
func TestStreamReply_StopDeliversPartial(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage("777")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	runCtx, cancelRun := context.WithCancel(context.Background())
	defer cancelRun()
	defer b.registerInflight(item.ID, item.ChatID, 0, cancelRun)()

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()

	chunks <- "half an "
	chunks <- "answer"
	waitFor(t, "draft with partial text", draftShowing(fake, "half an answer"))

	drafts := fake.callsTo("sendMessageDraft")
	last := drafts[len(drafts)-1]
	if last.Form["can_stop"] != "true" || last.Form["keep_on_stop"] != "true" {
		t.Errorf("draft flags = can_stop %q keep_on_stop %q, want true/true",
			last.Form["can_stop"], last.Form["keep_on_stop"])
	}
	var draftID int
	for _, c := range last.Form["draft_id"] {
		draftID = draftID*10 + int(c-'0')
	}

	b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, draftID))
	if runCtx.Err() == nil {
		t.Error("stop did not cancel the agent run context")
	}

	waitFor(t, "partial reply sent", func() bool { return len(fake.callsTo("sendMessage")) == 1 })
	// The agent may still emit output after the stop; it must not block.
	chunks <- " and more"
	close(chunks)
	result <- runner.RunResult{FullText: "half an answer and more"}

	if err := <-errc; !errors.Is(err, queue.ErrStopped) {
		t.Fatalf("streamReply err = %v, want queue.ErrStopped", err)
	}

	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 {
		t.Fatalf("sendMessage calls = %d, want 1", len(sends))
	}
	if got := sends[0].Form["text"]; !strings.Contains(got, "half an answer") || strings.Contains(got, "and more") {
		t.Errorf("final text = %q, want the partial text only", got)
	}
	row, err := b.store.GetTurn(context.Background(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	if row.ReplyMsgID != 777 {
		t.Errorf("reply_msg_id = %d, want 777", row.ReplyMsgID)
	}
	if b.inflightStop(item.ID) == nil {
		t.Error("streamReply must not unregister; runAgentStreaming owns the registration")
	}
}

// TestStreamReply_StopRacingStreamEndEndsStopped: the stop fired
// before the stream's end is processed, with Chunks already closed and
// Result already sent, so every select sees both ready. Whichever case
// wins, the turn must end stopped with exactly one final message.
func TestStreamReply_StopRacingStreamEndEndsStopped(t *testing.T) {
	for i := 0; i < 50; i++ {
		fake := &fakeTelegram{}
		fake.respond = func(method string, _ map[string]string) (int, string) {
			if method == "sendMessage" {
				return sentMessage("777")
			}
			return http.StatusOK, `{"ok":true,"result":true}`
		}
		b, item := newStreamFixture(t, fake)
		unregister := b.registerInflight(item.ID, item.ChatID, 0, func() {})
		b.bindDraft(item.ID, item.ChatID, 999)
		b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, 999))

		chunks := make(chan string)
		close(chunks)
		result := make(chan runner.RunResult, 1)
		result <- runner.RunResult{FullText: "the whole answer"}
		close(result)

		err := b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
		unregister()
		if !errors.Is(err, queue.ErrStopped) {
			t.Fatalf("iteration %d: err = %v, want queue.ErrStopped", i, err)
		}
		sends := fake.callsTo("sendMessage")
		if len(sends) != 1 {
			t.Fatalf("iteration %d: sendMessage calls = %d, want 1", i, len(sends))
		}
		if got := sends[0].Form["text"]; strings.Contains(got, "whole answer") {
			t.Fatalf("iteration %d: final text = %q, want the stopped reply, not the full one", i, got)
		}
		if got := countBotMessages(t, b, item); got != 1 {
			t.Fatalf("iteration %d: recorded replies = %d, want 1", i, got)
		}
	}
}

// TestStreamReply_StopWhileWaitingForResult: the stream's chunks end,
// then the stop lands while streamReply waits for Result.
func TestStreamReply_StopWhileWaitingForResult(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return sentMessage("777")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	b, item := newStreamFixture(t, fake)
	defer b.registerInflight(item.ID, item.ChatID, 0, func() {})()

	chunks := make(chan string)
	result := make(chan runner.RunResult, 1)
	errc := make(chan error, 1)
	go func() {
		errc <- b.streamReply(context.Background(), item, &runner.AgentStream{Chunks: chunks, Result: result})
	}()
	chunks <- "partial"
	waitFor(t, "draft with partial", draftShowing(fake, "partial"))
	drafts := fake.callsTo("sendMessageDraft")
	var draftID int
	for _, c := range drafts[len(drafts)-1].Form["draft_id"] {
		draftID = draftID*10 + int(c-'0')
	}
	close(chunks)
	time.Sleep(30 * time.Millisecond) // let streamReply leave the loop and block on Result
	b.handleGenerationStopped(context.Background(), stopUpdate(item.ChatID, draftID))

	select {
	case err := <-errc:
		if !errors.Is(err, queue.ErrStopped) {
			t.Fatalf("err = %v, want queue.ErrStopped", err)
		}
	case <-time.After(2 * time.Second):
		result <- runner.RunResult{FullText: "partial and the rest"}
		t.Fatalf("stop while waiting for Result was ignored (streamReply returned %v after Result)", <-errc)
	}
	result <- runner.RunResult{FullText: "partial and the rest"}
	close(result)
	sends := fake.callsTo("sendMessage")
	if len(sends) != 1 || strings.Contains(sends[0].Form["text"], "the rest") {
		t.Fatalf("sends = %+v, want one message with the partial text", sends)
	}
	if got := countBotMessages(t, b, item); got != 1 {
		t.Errorf("recorded replies = %d, want 1", got)
	}
}

// countBotMessages counts the rows recordReply persisted. The fixture
// stores no inbound messages, so every row is an outbound reply.
func countBotMessages(t *testing.T, b *Bot, _ queue.Item) int {
	t.Helper()
	n, err := b.store.CountMessages(context.Background())
	if err != nil {
		t.Fatalf("CountMessages: %v", err)
	}
	return int(n)
}
