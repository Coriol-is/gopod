package telegram

import (
	"context"
	"sync"
	"testing"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/go-telegram/bot/models"
)

func TestPreviewControlsCannotRetargetReplacement(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	item := queue.Item{ID: 42, ChatID: streamChat}
	oldCtx, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	oldKeyboard := b.turnKeyboard(oldCtx, item.ID).(*models.InlineKeyboardMarkup)
	currentCtx, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.bindDraft(currentCtx, item.ID, item.ChatID, 22)
	currentKeyboard := b.turnKeyboard(currentCtx, item.ID).(*models.InlineKeyboardMarkup)
	if oldKeyboard.InlineKeyboard[0][0].CallbackData == currentKeyboard.InlineKeyboard[0][0].CallbackData {
		t.Fatal("replacement reused Stop token")
	}
	for _, draft := range []int{33, 0} {
		b.bindDraft(oldCtx, item.ID, item.ChatID, draft)
		if b.turnKeyboard(oldCtx, item.ID) != nil {
			t.Fatal("old generation acquired successor keyboard")
		}
		b.inflightMu.Lock()
		got := b.inflight[item.ID].draftID
		b.inflightMu.Unlock()
		if got != 22 {
			t.Fatalf("old generation changed successor draft to %d", got)
		}
	}
	b.handleGenerationStopped(t.Context(), stopUpdate(item.ChatID, 33))
	select {
	case <-finalGenerationTurn(currentCtx).stopped:
		t.Fatal("obsolete draft stopped replacement")
	default:
	}
	b.handleGenerationStopped(t.Context(), stopUpdate(item.ChatID, 22))
	select {
	case <-finalGenerationTurn(currentCtx).stopped:
	default:
		t.Fatal("current draft cannot stop current generation")
	}
}

func TestPreviewControlsDoNotRecaptureMissingOrMismatchedBinding(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	item := queue.Item{ID: 42, ChatID: streamChat}
	missing, _, _ := b.finalHandoff(t.Context(), item.ID)
	current, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.bindDraft(current, item.ID, item.ChatID, 22)
	mismatch := context.WithValue(current, finalGenerationKey{}, finalGeneration{turnID: 99, turn: finalGenerationTurn(current)})
	for _, ctx := range []context.Context{missing, mismatch} {
		b.bindDraft(ctx, item.ID, item.ChatID, 33)
		if b.turnKeyboard(ctx, item.ID) != nil {
			t.Fatal("invalid binding acquired control")
		}
	}
	if finalGenerationTurn(current).draftID != 22 {
		t.Fatal("invalid binding changed draft")
	}
}

func TestPreviewControlsRaceWithReplacement(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	item := queue.Item{ID: 42, ChatID: streamChat}
	for range 100 {
		old, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 20 {
				b.bindDraft(old, item.ID, item.ChatID, 11)
				b.turnKeyboard(old, item.ID)
			}
		}()
		current, done := b.registerFinalGeneration(t.Context(), item, func() {})
		b.bindDraft(current, item.ID, item.ChatID, 22)
		wg.Wait()
		oldDone()
		if finalGenerationTurn(current).draftID != 22 || b.turnKeyboard(current, item.ID) == nil {
			t.Fatal("old handler changed current controls")
		}
		done()
	}
}

func TestFinalOwnerCannotAcquirePreviewControls(t *testing.T) {
	b := newFakeBot(t, &fakeTelegram{})
	item := queue.Item{ID: 42, ChatID: streamChat}
	ctx, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.bindDraft(ctx, item.ID, item.ChatID, 22)
	if err := finalGenerationTurn(ctx).beginFinal(); err != nil {
		t.Fatal(err)
	}
	b.bindDraft(ctx, item.ID, item.ChatID, 33)
	if finalGenerationTurn(ctx).draftID != 22 || b.turnKeyboard(ctx, item.ID) != nil {
		t.Fatal("final owner acquired preview controls")
	}
}
