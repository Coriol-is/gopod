package telegram

import (
	"context"
	"errors"
	"strconv"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestTopicQuiescenceJoinsUnknownEditAndKeepsSiblings(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		done <- b.topicEditRequest(context.Background(), streamChat, 999, func(context.Context) error { close(started); <-release; return nil })
	}()
	<-started
	d, _, _ = b.store.BeginTopicDeletion(t.Context(), d.Scope, 7)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := b.QuiesceTopic(ctx, d); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("early quiescence: %v", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if err := b.QuiesceTopic(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	calls := 0
	for _, thread := range []int{0, 8} {
		ctx := context.WithValue(t.Context(), topicOutputOriginKey{}, chatThread{streamChat, thread})
		if err := b.topicRequest(ctx, streamChat, thread, func(context.Context) error { calls++; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := b.topicEditRequest(t.Context(), streamChat, 1000, func(context.Context) error { calls++; return nil }); !errors.Is(err, runner.ErrConversationQuiescing) {
		t.Fatalf("unknown admitted: %v", err)
	}
	if calls != 2 {
		t.Fatalf("calls %d", calls)
	}
}
func TestTopicQuiescenceUnknownEditAmbiguitySurvivesJoin(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	if err := b.topicEditRequest(t.Context(), streamChat, 555, func(context.Context) error { return errors.New("lost acknowledgement") }); err == nil {
		t.Fatal("missing error")
	}
	d, _, _ = b.store.BeginTopicDeletion(t.Context(), d.Scope, 7)
	if err := b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicTransportUncertain) {
		t.Fatalf("ambiguity cleared: %v", err)
	}
}
func TestTopicQuiescenceRestoreNeverProvesOldRequestAbsent(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	if err := b.RestoreTopicFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicRestartReconciliation) {
		t.Fatalf("restored proof: %v", err)
	}
	if _, _, err := b.topicIngress.Enter(t.Context(), d.Scope.JID, 7); !errors.Is(err, runner.ErrConversationQuiescing) {
		t.Fatal(err)
	}
}
func TestTopicQuiescenceRefusalReopensExactIntent(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	if err := b.QuiesceTopic(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	if err := b.ReopenRefusedTopic(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	_, release, err := b.topicTransport.Enter(t.Context(), strconv.FormatInt(streamChat, 10), 7)
	if err != nil {
		t.Fatal(err)
	}
	release()
	next, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.QuiesceTopic(t.Context(), next); err != nil {
		t.Fatal(err)
	}
	if err = b.ReopenRefusedTopic(t.Context(), d); !errors.Is(err, store.ErrTopicDeletionState) {
		t.Fatalf("stale reopen %v", err)
	}
	if _, _, err = b.topicTransport.Enter(t.Context(), strconv.FormatInt(streamChat, 10), 7); !errors.Is(err, runner.ErrConversationQuiescing) {
		t.Fatal(err)
	}
}
func TestTopicQuiescenceKnownEditsBoundedAndSourceFenced(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	for i := 1; i <= 5000; i++ {
		b.rememberTopicMessage(t.Context(), streamChat, 8, i)
	}
	if len(b.topicMessages) != 4096 || len(b.topicMessageOrder) != 4096 {
		t.Fatal("unbounded owners")
	}
	b.rememberTopicMessage(t.Context(), streamChat, 7, 6000)
	called := false
	if err := b.topicEditRequest(t.Context(), streamChat, 6000, func(context.Context) error { called = true; return nil }); !errors.Is(err, store.ErrTopicDeleting) || called {
		t.Fatalf("edit %v %v", err, called)
	}
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.topicRequest(canceled, streamChat, 7, func(context.Context) error { t.Fatal("canceled dispatch"); return nil }); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if b.topicUncertain[chatThread{streamChat, 7}] {
		t.Fatal("predispatch cancellation marked ambiguous")
	}
}

func TestTopicCallbackMainFallbackRetainsHostSource(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	b.rememberTopicMessage(withTopicOutputOrigin(t.Context(), streamChat, 7), streamChat, 0, 321)
	calls := 0
	b.onCallback("probe", func(ctx context.Context, q cbQuery) (string, error) {
		err := b.topicEditRequest(ctx, q.ChatID, q.MessageID, func(context.Context) error { calls++; return nil })
		return "ok", err
	})
	update := cbUpdate("fallback", streamChat, 0, 321, "probe")
	if got := b.dispatchCallback(t.Context(), update.CallbackQuery, func(string) {}); got != "ok" || calls != 1 {
		t.Fatalf("fallback control %q %d", got, calls)
	}
	if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
		t.Fatal(err)
	}
	if got := b.dispatchCallback(t.Context(), update.CallbackQuery, func(string) {}); got != cbToastExpired || calls != 1 {
		t.Fatalf("fenced fallback %q %d", got, calls)
	}
}

func TestTopicQuiescenceRefusesActiveInteractiveLogin(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	b.logins = newLoginSessions()
	b.logins.set(streamChat, &loginSession{chatID: streamChat, thread: 0, private: true, awaitsCode: true, topicOrigin: chatThread{streamChat, 7}})
	if err := b.QuiesceTopic(t.Context(), d); !errors.Is(err, ErrTopicLoginActive) {
		t.Fatalf("live OAuth native login ignored: %v", err)
	}
	b.logins.set(streamChat, &loginSession{chatID: streamChat, thread: 8, private: true, topicOrigin: chatThread{streamChat, 8}})
	if err := b.QuiesceTopic(t.Context(), d); err != nil {
		t.Fatalf("unrelated login blocked topic: %v", err)
	}
}
