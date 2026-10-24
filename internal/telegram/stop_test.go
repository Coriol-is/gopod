package telegram

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

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

	defer b.registerInflight(1, cancelA)()
	defer b.registerInflight(2, cancelB)()
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

	unregister := b.registerInflight(1, cancel)
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
	defer b.registerInflight(1, func() { atomic.AddInt32(&cancels, 1) })()
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
	defer b.registerInflight(1, cancel)()
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
	defer b.registerInflight(item.ID, cancelRun)()

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
