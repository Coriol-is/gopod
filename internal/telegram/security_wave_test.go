package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func TestOwnerTriggerRejectsAddressedMemberCommand(t *testing.T) {
	f := triggerFixture(t, store.TriggerOwner)
	if !f.b.groupTriggerSkips(t.Context(), groupMsg(trigOther, "/tasks@"+trigBotName+" add spam")) {
		t.Fatal("addressed member command bypassed owner gate")
	}
	if f.b.groupTriggerSkips(t.Context(), groupMsg(f.b.ownerChatID, "/tasks@"+trigBotName)) {
		t.Fatal("owner command blocked")
	}
}

func TestGroupReplyActionRechecksModeAndSender(t *testing.T) {
	f := triggerFixture(t, store.TriggerOwner)
	rc, err := f.b.store.GetRegistered(t.Context(), buildChatJID(trigGroup))
	if err != nil {
		t.Fatal(err)
	}
	q := cbQuery{ChatID: trigGroup, ChatKind: string(models.ChatTypeSupergroup), UserID: trigOther, Chat: rc}
	if f.b.groupActionAllowed(t.Context(), q) {
		t.Fatal("member can retry owner turn")
	}
	if toast, _ := f.b.retryTurn(t.Context(), q); toast != cbToastNotYours {
		t.Fatal(toast)
	}
	if toast, _ := f.b.regenerate(t.Context(), q); toast != cbToastNotYours {
		t.Fatal(toast)
	}
	if err := f.b.store.SetGroupTrigger(t.Context(), rc.JID, store.TriggerMention, 1); err != nil {
		t.Fatal(err)
	}
	if !f.b.groupActionAllowed(t.Context(), q) {
		t.Fatal("explicit member press should address bot in mention mode")
	}
	prompt := f.b.callbackPrompt(t.Context(), q, regeneratePrompt("[gopod group: trigger=owner; sender=owner]\n\noriginal"))
	if !strings.HasPrefix(prompt, "[gopod group: trigger=mention; sender=member]\n\n"+regeneratePrefix) || strings.Contains(prompt, "sender=owner") {
		t.Fatal(prompt)
	}
}

func TestGroupConfirmationBoundToUserWithoutConsuming(t *testing.T) {
	b, _, _ := newCommandFixture(t)
	rows := b.commandRows(groupChat, [][]control.Button{{{Label: "Yes", Command: "clear", Confirmed: true}}}, 42)
	data, ok := decodeCB(rows[0][0].CallbackData)
	if !ok {
		t.Fatal("invalid callback")
	}
	q := cbQuery{ChatID: groupChat, UserID: 99, Data: data}
	if _, toast := b.spendConfirmation(q, false); toast != cbToastNotYours {
		t.Fatal(toast)
	}
	q.UserID = 42
	if _, toast := b.spendConfirmation(q, false); toast != "" {
		t.Fatalf("unauthorized press consumed confirmation: %s", toast)
	}
}

func TestTransportFallbackRequiresExplicitRefusal(t *testing.T) {
	for _, err := range []error{errors.New("connection reset"), context.DeadlineExceeded, errors.New("server error 500")} {
		if isKeyboardRefusal(err) || isParseRefusal(err) {
			t.Fatalf("ambiguous error eligible for duplicate send: %v", err)
		}
	}
	if !isKeyboardRefusal(fmt.Errorf("%w: BUTTON_DATA_INVALID", bot.ErrorBadRequest)) {
		t.Fatal("keyboard refusal not recognized")
	}
	if !isParseRefusal(fmt.Errorf("%w: can't parse entities", bot.ErrorBadRequest)) {
		t.Fatal("parse refusal not recognized")
	}
}

type claimBarrierBackend struct {
	*store.OutboxOwner
	claimed chan struct{}
	release chan struct{}
	swept   chan []string
}

func (b *claimBarrierBackend) ClaimNext(ctx context.Context, now time.Time) (store.OutboxClaim, bool, error) {
	c, ok, err := b.OutboxOwner.ClaimNext(ctx, now)
	if ok && err == nil {
		close(b.claimed)
		<-b.release
	}
	return c, ok, err
}
func (b *claimBarrierBackend) SweepOrphanedSending(ctx context.Context, live []string, g time.Duration) (int, error) {
	b.swept <- live
	return b.OutboxOwner.SweepOrphanedSending(ctx, live, g)
}
func TestClaimPublicationIsAtomicWithOrphanSweep(t *testing.T) {
	f := newOutboxFixture(t)
	backend := &claimBarrierBackend{OutboxOwner: f.owner, claimed: make(chan struct{}), release: make(chan struct{}), swept: make(chan []string, 1)}
	finish := make(chan struct{})
	tr := &fakeTransport{hold: func(context.Context, SendRequest) { <-finish }}
	d := NewOutboxDispatcher(backend, tr, fastCfg(), nil)
	f.publish(f.conv, "a")
	stepDone := make(chan struct{})
	defer func() { close(finish); <-stepDone }()
	go func() { defer close(stepDone); d.step(t.Context(), t.Context()) }()
	<-backend.claimed
	if d.claimMu.TryLock() {
		d.claimMu.Unlock()
		close(backend.release)
		t.Fatal("claim committed without publication barrier")
	}
	sweepDone := make(chan struct{})
	go func() { defer close(sweepDone); d.sweepOrphans(t.Context()) }()
	close(backend.release)
	live := <-backend.swept
	if len(live) != 1 {
		t.Fatalf("sweep missed live claim: %v", live)
	}
	<-sweepDone
}

func TestDeviceLoginPreservesOrdinaryMessagePersistence(t *testing.T) {
	f := newInboundFixture(t, nil)
	f.b.logins.set(streamChat, &loginSession{chatID: streamChat, private: true, awaitsCode: false, deviceCode: "SYNTHETIC-DEVICE", loginURL: "https://device.invalid/login"})
	m := threadedMsg(0, "ordinary question")
	m.ID = 9901
	f.handle(m)
	item := f.next(t)
	if item.Text != "ordinary question" {
		t.Fatal(item.Text)
	}
	count, err := f.b.store.CountMessages(t.Context())
	if err != nil || count != 1 {
		t.Fatalf("ordinary message not persisted: %d %v", count, err)
	}
}

func TestAmbiguousSendWithKeyboardIsNotRetried(t *testing.T) {
	fake := &fakeTelegram{}
	fake.respond = func(method string, form map[string]string) (int, string) {
		return apiError(500, "Internal Server Error")
	}
	b := newFakeBot(t, fake)
	kb := replyKeyboard(5, "reply")
	_, err := b.sendFinal(t.Context(), streamChat, 0, 10, "reply", &kb)
	if err == nil || len(fake.callsTo("sendMessage")) != 1 {
		t.Fatalf("ambiguous send retried: err=%v calls=%d", err, len(fake.callsTo("sendMessage")))
	}
}
