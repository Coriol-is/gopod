package telegram

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestWaitGenerationCleanupCannotConsumeSuccessor(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	old, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	current, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	oldKey, newKey := b.waitKeyOf(old, item), b.waitKeyOf(current, item)
	oldQuit, oldJoined := make(chan struct{}), make(chan struct{})
	newQuit, newJoined := make(chan struct{}), make(chan struct{})
	close(oldJoined)
	close(newJoined)
	b.waitPlaceholders = map[waitKey]int{oldKey: 11, newKey: 22}
	b.waitRefresh = map[waitKey]waitRefresher{oldKey: {quit: oldQuit, done: oldJoined}, newKey: {quit: newQuit, done: newJoined}}
	if got := b.popWaitPlaceholder(old, item); got != 11 {
		t.Fatal(got)
	}
	b.stopWaitDraftRefresh(old, item)
	if got := b.waitPlaceholder(current, item); got != 22 {
		t.Fatalf("successor placeholder lost: %d", got)
	}
	select {
	case <-newQuit:
		t.Fatal("old cleanup stopped successor refresher")
	default:
	}
	if b.waitPlaceholder(old, item) != 0 {
		t.Fatal("old key recaptured successor")
	}
	b.stopWaitDraftRefresh(current, item)
	if got := b.popWaitPlaceholder(current, item); got != 22 {
		t.Fatal(got)
	}
}

func TestWaitGenerationRefresherDuplicateAndConcurrentJoin(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	ctx, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	key := b.waitKeyOf(ctx, item)
	quit, joined := make(chan struct{}), make(chan struct{})
	b.waitRefresh = map[waitKey]waitRefresher{key: {quit: quit, done: joined}}
	b.startWaitDraftRefresh(ctx, item, 5, "wait", draftOpts{}, time.Time{})
	if b.waitRefresh[key].done != joined {
		t.Fatal("duplicate start replaced unjoined refresher")
	}
	returned := make(chan struct{}, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); b.stopWaitDraftRefresh(ctx, item); returned <- struct{}{} }()
	}
	<-quit
	select {
	case <-returned:
		t.Fatal("stop returned before refresher joined")
	case <-time.After(20 * time.Millisecond):
	}
	close(joined)
	wg.Wait()
	if len(returned) != 2 || len(b.waitRefresh) != 0 {
		t.Fatal("join did not complete or clear state")
	}
}

func TestWaitGenerationSealedRefresherExits(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	b.waitDraftEvery = time.Millisecond
	old, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	_, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.startWaitDraftRefresh(old, item, 5, "wait", draftOpts{}, time.Time{})
	b.waitMu.Lock()
	rf := b.waitRefresh[b.waitKeyOf(old, item)]
	b.waitMu.Unlock()
	select {
	case <-rf.done:
	case <-time.After(time.Second):
		t.Fatal("sealed refresher kept running")
	}
	b.stopWaitDraftRefresh(old, item)
}

func TestControlGenerationRejectsLatePlaceholder(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(item.ChatID), Folder: item.Folder, Name: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	old, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	b.recordPlaceholder(old, item, 11)
	current, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.recordPlaceholder(current, item, 22)
	b.recordPlaceholder(old, item, 33)
	turn, err := b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.PlaceholderMsgID != 22 {
		t.Fatal(turn.PlaceholderMsgID, err)
	}
	first, err := b.store.GetControlMessage(t.Context(), item.ChatID, 11)
	if err != nil || first.Token != finalGenerationTurn(old).token {
		t.Fatal("original binding changed", err)
	}
	next, err := b.store.GetControlMessage(t.Context(), item.ChatID, 22)
	if err != nil || next.Token != finalGenerationTurn(current).token {
		t.Fatal("successor binding wrong", err)
	}
	if _, err := b.store.GetControlMessage(t.Context(), item.ChatID, 33); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("late binding persisted", err)
	}
	b.inflightMu.Lock()
	claimed := finalGenerationTurn(current).messages[33]
	b.inflightMu.Unlock()
	if claimed {
		t.Fatal("late message entered successor Stop allowlist")
	}
	// An explicitly empty captured binding cannot acquire a subsequently registered turn.
	missing := context.WithValue(t.Context(), finalGenerationKey{}, finalGeneration{turnID: item.ID})
	b.recordPlaceholder(missing, item, 44)
	turn, err = b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.PlaceholderMsgID != 22 {
		t.Fatal("empty binding overwrote placeholder", err)
	}
}

func TestWaitGenerationLateHTTPDoesNotPublishSuccessorMetadata(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	fake := &fakeTelegram{respond: func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" {
			close(entered)
			<-release
			return sentMessage("11")
		}
		return okResponder(method, form)
	}}
	b, item := newStreamFixture(t, fake)
	b.markDraftsUnsupported(item.ChatID)
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(item.ChatID), Folder: item.Folder, Name: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	old, oldDone := b.registerFinalGeneration(t.Context(), item, func() {})
	defer oldDone()
	finished := make(chan struct{})
	go func() { defer close(finished); b.waitNotice(old, item)(runner.WaitSlot) }()
	<-entered
	current, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.recordPlaceholder(current, item, 22)
	b.waitMu.Lock()
	if b.waitPlaceholders == nil {
		b.waitPlaceholders = make(map[waitKey]int)
	}
	b.waitPlaceholders[b.waitKeyOf(current, item)] = 22
	b.waitMu.Unlock()
	close(release)
	<-finished
	if got := b.waitPlaceholder(current, item); got != 22 {
		t.Fatal("late HTTP replaced successor cache", got)
	}
	turn, err := b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.PlaceholderMsgID != 22 {
		t.Fatal("late HTTP replaced durable memo", err)
	}
	if _, err := b.store.GetControlMessage(t.Context(), item.ChatID, 11); !errors.Is(err, sql.ErrNoRows) {
		t.Fatal("late HTTP bound old message as active control", err)
	}
	if got := b.popWaitPlaceholder(old, item); got != 11 {
		t.Fatal("old result not isolated for cleanup", got)
	}
	if got := b.popWaitPlaceholder(current, item); got != 22 {
		t.Fatal("cleanup consumed successor", got)
	}
}

func TestControlGenerationUnregisteredPlaceholderCompatibility(t *testing.T) {
	b, item := newStreamFixture(t, &fakeTelegram{respond: okResponder})
	ctx, _, _ := b.finalHandoff(t.Context(), item.ID)
	b.recordPlaceholder(ctx, item, 11)
	turn, err := b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.PlaceholderMsgID != 11 {
		t.Fatal("unregistered placeholder lost", err)
	}
	current, done := b.registerFinalGeneration(t.Context(), item, func() {})
	defer done()
	b.recordPlaceholder(current, item, 22)
	b.recordPlaceholder(ctx, item, 33)
	turn, err = b.store.GetTurn(t.Context(), item.ID)
	if err != nil || turn.PlaceholderMsgID != 22 {
		t.Fatal("unregistered context captured successor", err)
	}
}
