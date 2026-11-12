package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func foregroundFixture(t *testing.T) (*Bot, *fakeTelegram, queue.Item, context.Context) {
	t.Helper()
	fake := seqFake()
	b, item := newStreamFixture(t, fake)
	enablePrepared(t, b, recSpeech{&ttsRecorder{audio: []byte("OggS-audio")}})
	item.Attempts = 1
	item.IsOwner = true
	if err := b.store.MarkTurnRunning(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	if err := b.EnableForegroundDelivery(); err != nil {
		t.Fatal(err)
	}
	ctx, err := b.bindOrdinaryBatch(t.Context(), []queue.Item{item})
	if err != nil {
		t.Fatal(err)
	}
	if !usesForegroundDelivery(ctx) {
		t.Fatal("owner batch was not bound")
	}
	return b, fake, item, ctx
}
func freezeForeground(t *testing.T, b *Bot, ctx context.Context, text string) store.TurnResult {
	t.Helper()
	binding := ctx.Value(ordinaryBatchKey{}).(store.OrdinaryStopBinding)
	r, err := b.store.FreezeTurnResult(ctx, store.ExecutionRef{Conversation: binding.Conversation, Kind: store.ExecInteractive, Members: binding.Members, Provider: "claude"}, text)
	if err != nil {
		t.Fatal(err)
	}
	return r
}
func TestForegroundSyncPublishesWithoutDirectFinal(t *testing.T) {
	b, fake, item, ctx := foregroundFixture(t)
	calls := 0
	b.syncRun = func(run context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
		calls++
		freezeForeground(t, b, run, "final durable answer")
		return "final durable answer", nil
	}
	if err := b.runAgentSync(ctx, item); err != nil {
		t.Fatal(err)
	}
	r, ok, err := b.store.TurnResultForAttempt(t.Context(), item.ID, 1)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	intent, err := b.store.GetFinalReply(t.Context(), r.ID)
	if err != nil || intent.State != store.FinalReplyPublished {
		t.Fatal(intent, err)
	}
	for _, method := range []string{"sendMessage", "editMessageText"} {
		for _, call := range fake.callsTo(method) {
			if strings.Contains(call.Form["text"], "final durable answer") {
				t.Fatal("direct final escaped", call)
			}
		}
	}
	turn, err := b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.ReplyMsgID != 0 {
		t.Fatal(turn, err)
	}
	plan, err := b.store.RecoverOrdinaryTurn(t.Context(), item.ID)
	if err != nil || plan.Action != store.OrdinaryPublished || calls != 1 {
		t.Fatal(plan, err, calls)
	}
}
func TestForegroundMissingFrozenResultNeverFallsBack(t *testing.T) {
	b, fake, item, ctx := foregroundFixture(t)
	b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) {
		return "uncommitted result", nil
	}
	err := b.runAgentSync(ctx, item)
	if !noReplayError(err) {
		t.Fatal(err)
	}
	for _, call := range fake.callsTo("sendMessage") {
		if strings.Contains(call.Form["text"], "uncommitted result") {
			t.Fatal("unfrozen reply sent")
		}
	}
}
func TestForegroundStreamingRetiresOverflowInImmutablePlan(t *testing.T) {
	b, _, item, ctx := foregroundFixture(t)
	b.markDraftsUnsupported(item.ChatID)
	text := strings.Repeat("large answer ", 400)
	freezeForeground(t, b, ctx, text)
	ctx, unregister := b.registerFinalGeneration(ctx, item, func() {})
	defer unregister()
	chunks := make(chan string, 1)
	chunks <- text
	close(chunks)
	result := make(chan runner.RunResult, 1)
	result <- runner.RunResult{FullText: text}
	close(result)
	if err := b.streamReply(ctx, item, &runner.AgentStream{Chunks: chunks, Result: result}); err != nil {
		t.Fatal(err)
	}
	r, _, _ := b.store.TurnResultForAttempt(t.Context(), item.ID, 1)
	intent, err := b.store.GetFinalReply(t.Context(), r.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.store.GetDelivery(t.Context(), intent.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Parts) < 2 {
		t.Fatal(d)
	}
	// The final preview's Stop retirement is owned by outbox, before standalone text.
	if d.Parts[0].Part.Kind != store.PartEdit || d.Parts[0].Part.ReplyMarkup != `{"inline_keyboard":[]}` {
		t.Fatal(d.Parts[0])
	}
}
func TestForegroundStopBeforePublicationRemainsUnpublished(t *testing.T) {
	b, _, item, ctx := foregroundFixture(t)
	r := freezeForeground(t, b, ctx, "late answer")
	binding := ctx.Value(ordinaryBatchKey{}).(store.OrdinaryStopBinding)
	if _, err := recordStopIntent(ctx, b.store, binding); err != nil {
		t.Fatal(err)
	}
	err := b.publishForeground(ctx, item, FinalReplyPresentation{})
	if !noReplayError(err) || !errors.Is(err, store.ErrResultStopped) {
		t.Fatal(err)
	}
	if _, err = b.store.GetFinalReply(ctx, r.ID); err == nil {
		t.Fatal("stopped result acquired presentation")
	}
}
func TestFinalHandoffJoinsAlreadyAdmittedPreview(t *testing.T) {
	b, _, item, ctx := foregroundFixture(t)
	ctx, unregister := b.registerFinalGeneration(ctx, item, func() {})
	defer unregister()
	entered, release, previewDone := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		previewDone <- withGenerationPreview(ctx, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	_, handoff, _ := b.finalHandoff(ctx, item.ID)
	done := make(chan error, 1)
	go func() { done <- handoff(ctx) }()
	select {
	case err := <-done:
		t.Fatal("handoff crossed active preview", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(release)
	if err := <-previewDone; err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := withGenerationPreview(ctx, func() error { t.Fatal("new preview admitted"); return nil }); err == nil {
		t.Fatal("preview not sealed")
	}
}

func TestForegroundModeCapturedBeforeSettingsChange(t *testing.T) {
	b, _, item, ctx := foregroundFixture(t)
	SetReplyMode(item.ChatID, "voice")
	t.Cleanup(func() { SetReplyMode(item.ChatID, "") })
	r := freezeForeground(t, b, ctx, "text at admission")
	if err := b.publishForeground(ctx, item, FinalReplyPresentation{}); err != nil {
		t.Fatal(err)
	}
	intent, err := b.store.GetFinalReply(ctx, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.store.GetDelivery(ctx, intent.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Parts) != 1 || d.Parts[0].Part.Kind != store.PartText {
		t.Fatal("mutable voice setting changed bound presentation")
	}
}

func TestForegroundGoneTopicHoldsFrozenResultWithoutMainFallback(t *testing.T) {
	b, fake, item, ctx := foregroundFixture(t)
	// Main scope is used by the fixture; topicGone also supports explicit root
	// keys, but production only marks nonzero topics. Bind a real topic here.
	item.ThreadID = 7
	// Use a fresh durable item matching the topic; keep the helper's main result absent.
	next, _, err := b.store.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "topic-gone-2", ChatID: item.ChatID, ChatFolder: item.Folder, ThreadID: 7, Text: "topic"})
	if err != nil {
		t.Fatal(err)
	}
	item.ID = next
	if err = b.store.MarkTurnRunning(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	ctx, err = b.bindOrdinaryBatch(t.Context(), []queue.Item{item})
	if err != nil {
		t.Fatal(err)
	}
	freezeForeground(t, b, ctx, "held answer")
	b.markThreadGone(item.ChatID, 7)
	before := len(fake.callsTo("sendMessage"))
	err = b.publishForeground(ctx, item, FinalReplyPresentation{})
	if !noReplayError(err) || !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatal(err)
	}
	if len(fake.callsTo("sendMessage")) != before {
		t.Fatal("rerouted frozen answer")
	}
}

func TestForegroundBufferedVoiceAndTextKeepOnePreparationOwner(t *testing.T) {
	for _, mode := range []string{"voice", "voice+text", "text"} {
		t.Run(mode, func(t *testing.T) {
			t.Setenv("OPENAI_API_KEY", "fake")
			b, fake, item, _ := foregroundFixture(t)
			SetReplyMode(item.ChatID, mode)
			t.Cleanup(func() { SetReplyMode(item.ChatID, "") })
			ctx, err := b.bindOrdinaryBatch(t.Context(), []queue.Item{item})
			if err != nil {
				t.Fatal(err)
			}
			b.syncRun = func(run context.Context, _ string, _ int, _ runner.Tier, _ string) (string, error) {
				freezeForeground(t, b, run, "voice and text")
				return "voice and text", nil
			}
			if err = b.runAgentSync(ctx, item); err != nil {
				t.Fatal(err)
			}
			r, _, _ := b.store.TurnResultForAttempt(ctx, item.ID, 1)
			intent, err := b.store.GetFinalReply(ctx, r.ID)
			if err != nil {
				t.Fatal(err)
			}
			d, err := b.store.GetDelivery(ctx, intent.DeliveryID)
			if err != nil {
				t.Fatal(err)
			}
			voices, texts := 0, 0
			for _, p := range d.Parts {
				if p.Part.ParseMode == store.UploadPresentationVoice {
					voices++
				}
				if p.Part.Kind == store.PartText || p.Part.Kind == store.PartEdit {
					texts++
				}
			}
			wantVoice := 0
			if mode != "text" {
				wantVoice = 1
			}
			wantText := 1
			if mode == "voice" {
				wantText = 0
			}
			if voices != wantVoice || texts != wantText {
				t.Fatalf("mode=%s voice=%d text=%d", mode, voices, texts)
			}
			if len(fake.callsTo("sendVoice")) != 0 {
				t.Fatal("direct voice escaped durable owner")
			}
		})
	}
}
