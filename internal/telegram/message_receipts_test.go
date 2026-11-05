package telegram

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func failReceiptWrite(t *testing.T, b *Bot, fake *fakeTelegram, id int) {
	t.Helper()
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			_ = b.store.Close()
			return sentMessage(strconv.Itoa(id))
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "synthetic"})
	var own *ownershipSendError
	if !errors.As(err, &own) || !own.receiptBacked {
		t.Fatalf("not receipt backed: %v", err)
	}
}
func reconnectReceiptStore(t *testing.T, b *Bot, path string) {
	t.Helper()
	_ = b.store.Close()
	var err error
	b.store, err = store.Open(t.Context(), path, nil)
	if err != nil {
		t.Fatal(err)
	}
}
func TestMessageReceiptRepairEnablesQuiescenceWithoutHTTP(t *testing.T) {
	for _, trigger := range []string{"quiescence", "control_loop", "lookup"} {
		t.Run(trigger, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, path, scope := ownershipFixture(t, fake)
			failReceiptWrite(t, b, fake, 711)
			if b.topicUncertain[chatThread{streamChat, 7}] {
				t.Fatal("receipt was latched generically")
			}
			reconnectReceiptStore(t, b, path)
			d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
			if err != nil {
				t.Fatal(err)
			}
			if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
				t.Fatal("missing pre-repair blocker", err)
			}
			switch trigger {
			case "control_loop":
				if err = b.RepairControls(t.Context()); err != nil {
					t.Fatal(err)
				}
			case "lookup":
				origin, known, e := b.messageOwner(t.Context(), streamChat, 711)
				if e != nil || !known || origin.thread != 7 {
					t.Fatalf("lookup %+v %v %v", origin, known, e)
				}
			}
			if err = b.QuiesceTopic(t.Context(), d); err != nil {
				t.Fatal(err)
			}
			if len(b.messageReceipts) != 0 {
				t.Fatal("settled receipt retained")
			}
			if len(fake.callsTo("sendMessage")) != 1 || len(fake.callsTo("editMessageText")) != 0 {
				t.Fatal("repair invoked Telegram")
			}
		})
	}
}
func TestMessageReceiptRestartCannotReconstructPositiveReceipt(t *testing.T) {
	fake := &fakeTelegram{}
	b, path, scope := ownershipFixture(t, fake)
	failReceiptWrite(t, b, fake, 712)
	reopenOwnership(t, b, path)
	if err := b.ReconcileMessageReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatal("invented crash evidence", err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("restart replayed")
	}
}
func TestMessageReceiptRepairKeepsOtherEvidence(t *testing.T) {
	fake := &fakeTelegram{}
	b, _, scope := ownershipFixture(t, fake)
	a, err := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	second, err := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	b.retainMessageReceipt(a, 721)
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatal("second unknown forgotten", err)
	}
	b.retainMessageReceipt(second, 722)
	b.topicUncertain = map[chatThread]bool{{streamChat, 7}: true, {streamChat, -1}: true}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicTransportUncertain) {
		t.Fatal("generic uncertainty cleared", err)
	}
	if !b.topicUncertain[chatThread{streamChat, 7}] || !b.topicUncertain[chatThread{streamChat, -1}] {
		t.Fatal("generic latch mutated")
	}
	if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); err != nil {
		t.Fatal(err)
	}
}
func TestMessageReceiptConcurrentRepairAndLateReturn(t *testing.T) {
	fake := &fakeTelegram{}
	b, _, _ := ownershipFixture(t, fake)
	o, err := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
	if err != nil {
		t.Fatal(err)
	}
	err = b.topicRequest(t.Context(), streamChat, 7, func(context.Context) error {
		retained := b.retainMessageReceipt(o, 731)
		var wg sync.WaitGroup
		for i := 0; i < 8; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				e := b.ReconcileMessageReceipts(t.Context())
				if e != nil && !errors.Is(e, store.ErrMessageOwnership) {
					t.Error(e)
				}
			}()
		}
		wg.Wait()
		return &ownershipSendError{attempted: true, receiptBacked: retained, cause: errors.New("original ack failed")}
	})
	if !noReplayError(err) {
		t.Fatal(err)
	}
	if b.topicUncertain[chatThread{streamChat, 7}] {
		t.Fatal("late return re-poisoned repaired topic")
	}
	if _, err = b.store.TelegramMessageOwner(t.Context(), streamChat, 731); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("repair sent HTTP")
	}
}
func TestMessageReceiptBoundsAndRetryBackoff(t *testing.T) {
	fake := &fakeTelegram{}
	b, _, scope := ownershipFixture(t, fake)
	for i := 0; i < messageReceiptLimit+1; i++ {
		o, err := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
		if err != nil {
			t.Fatal(err)
		}
		if got := b.retainMessageReceipt(o, 800+i); got != (i < messageReceiptLimit) {
			t.Fatalf("capacity %d %v", i, got)
		}
	}
	if len(b.messageReceipts) != messageReceiptLimit {
		t.Fatal("unbounded registry")
	}
	if err := b.ReconcileMessageReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatal("full-cache attempt lost", err)
	}
	// A stale/invalid receipt is retained with bounded retry, never repeatedly retried.
	bad := store.TelegramMessageOwner{AttemptID: "not-an-attempt", ChatID: streamChat, Scope: scope}
	b.retainMessageReceipt(bad, 9999)
	if err = b.ReconcileMessageReceipts(t.Context()); err == nil {
		t.Fatal("bad receipt settled")
	}
	b.receiptMu.Lock()
	r := b.messageReceipts[bad.AttemptID]
	b.receiptMu.Unlock()
	if !r.retryAt.After(time.Now()) || r.repairing {
		t.Fatal("missing retry bound")
	}
	if err = b.ReconcileMessageReceipts(t.Context()); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatal("backoff bypass", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err = b.ReconcileMessageReceipts(canceled); err == nil {
		t.Fatal("cancellation ignored")
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("repair sent HTTP")
	}
}

func TestMessageReceiptRepairBudgetReleasesClaims(t *testing.T) {
	fake := &fakeTelegram{}
	b, _, scope := ownershipFixture(t, fake)
	for i := 0; i < 3; i++ {
		o, err := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
		if err != nil {
			t.Fatal(err)
		}
		b.retainMessageReceipt(o, 1100+i)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	entered, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- b.store.WithRefusedTopicFence(t.Context(), d, func() error { close(entered); <-release; return nil })
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = b.ReconcileMessageReceipts(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("budget error %v", err)
	}
	if time.Since(started) > time.Second {
		t.Error("repair exceeded caller budget")
	}
	close(release)
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	b.receiptMu.Lock()
	defer b.receiptMu.Unlock()
	for _, r := range b.messageReceipts {
		if r.repairing {
			t.Error("cancel left claimed receipt")
		}
	}
}

func TestMessageReceiptFullRegistryKeepsDurableAndGenericHold(t *testing.T) {
	fake := &fakeTelegram{}
	b, path, scope := ownershipFixture(t, fake)
	for i := 0; i < messageReceiptLimit; i++ {
		o, e := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 0)
		if e != nil {
			t.Fatal(e)
		}
		b.retainMessageReceipt(o, 2000+i)
	}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			_ = b.store.Close()
			return sentMessage("3000")
		}
		return http.StatusOK, `{"ok":true,"result":true}`
	}
	_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "capacity probe"})
	var own *ownershipSendError
	if !errors.As(err, &own) || own.receiptBacked || !b.topicUncertain[chatThread{streamChat, 7}] {
		t.Fatalf("full registry lost hold: %v", err)
	}
	reconnectReceiptStore(t, b, path)
	if err = b.ReconcileMessageReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
		t.Fatal("unretained pending lost", err)
	}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicTransportUncertain) {
		t.Fatal("overflow generic hold cleared", err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("receipt repair replayed HTTP")
	}
}
func TestMessageReceiptUnknownNetworkHasNoPositiveEvidence(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return apiError(500, "unknown") }}
	b, _, scope := ownershipFixture(t, fake)
	_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, Text: "unknown"})
	if !noReplayError(err) || len(b.messageReceipts) != 0 {
		t.Fatal("unknown synthesized receipt")
	}
	if err = b.ReconcileMessageReceipts(t.Context()); err != nil {
		t.Fatal(err)
	}
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicTransportUncertain) {
		t.Fatal(err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("unknown resent")
	}
}
