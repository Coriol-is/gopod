package telegram

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestDeleteTopicOutcomesAndNoReplay(t *testing.T) {
	for _, tc := range []struct {
		name, state string
		code        int
		ok          bool
	}{{"success", "retired", 200, true}, {"refused", "refused", 400, false}, {"unknown", "uncertain", 500, false}, {"false", "uncertain", 200, false}} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
				if method == "deleteForumTopic" {
					if tc.code != 200 {
						return apiError(tc.code, "test response")
					}
					if tc.ok {
						return 200, `{"ok":true,"result":true}`
					}
					return 200, `{"ok":true,"result":false}`
				}
				return sentMessage("999")
			}}
			b, d := topicFenceFixture(t, fake)
			err := b.deleteTopic(t.Context(), d)
			if (tc.state == "retired") != (err == nil) {
				t.Fatal(err)
			}
			got, found, e := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
			if e != nil || !found || got.State != tc.state {
				t.Fatal(got, found, e)
			}
			calls := fake.callsTo("deleteForumTopic")
			if len(calls) != 1 || calls[0].Form["chat_id"] != strconv.FormatInt(streamChat, 10) || calls[0].Form["message_thread_id"] != "7" {
				t.Fatal(calls)
			}
			_ = b.deleteTopic(t.Context(), d)
			if len(fake.callsTo("deleteForumTopic")) != 1 {
				t.Fatal("remote call replayed")
			}
			if err = b.topicAdmission(t.Context(), streamChat, 8); err != nil {
				t.Fatal("sibling fenced", err)
			}
			if err = b.topicAdmission(t.Context(), streamChat, 0); err != nil {
				t.Fatal("main fenced", err)
			}
		})
	}
}
func TestDeleteTopicWaitsForCommandIngress(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "deleteForumTopic" {
			return 200, `{"ok":true,"result":true}`
		}
		return sentMessage("999")
	}}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	_, release, err := b.topicIngress.Enter(t.Context(), d.Scope.JID, 7)
	if err != nil {
		t.Fatal(err)
	}
	b.topicDeleteJobs = make(chan store.TopicDeletion, 1)
	if err = b.RequestTopicDeletion(t.Context(), d.Scope, 7); err != nil {
		t.Fatal(err)
	}
	next := <-b.topicDeleteJobs
	done := make(chan error, 1)
	go func() { done <- b.deleteTopic(t.Context(), next) }()
	if len(fake.callsTo("deleteForumTopic")) != 0 {
		t.Fatal("called before ingress join")
	}
	release()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
}
func TestDeleteTopicRestoredIntentNotReclassified(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.ClaimTopicDeletionDispatch(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreTopicFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.deleteTopic(t.Context(), d); !errors.Is(err, ErrTopicRestartReconciliation) {
		t.Fatal(err)
	}
	got, _, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "deleting" {
		t.Fatal(got, err)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 {
		t.Fatal("restored request replayed")
	}
}
func TestConfirmedDeletedTopicRecoveryIsLocalOnly(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreTopicFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.recoverDeletedTopics(context.Background())
	got, _, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "retired" {
		t.Fatal(got, err)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 || len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("recovery repeated remote effect")
	}
}

func TestTopicDeleteExactReceiptRepairsWithoutHTTP(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "deleteForumTopic" {
			return 200, `{"ok":true,"result":true}`
		}
		return sentMessage("999")
	}}
	b, d := topicFenceFixture(t, fake)
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_topic_ack BEFORE UPDATE OF state ON topic_deletions BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := b.deleteTopic(t.Context(), d); err == nil {
		t.Fatal("missing ack error")
	}
	b.topicMu.Lock()
	n := len(b.topicDeleteReceipts)
	b.topicMu.Unlock()
	if n != 1 {
		t.Fatal("receipt lost", n)
	}
	got, _, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "deleting" {
		t.Fatal(got, err)
	}
	if _, err = b.store.DB().Exec(`DROP TRIGGER fail_topic_ack`); err != nil {
		t.Fatal(err)
	}
	b.reconcileTopicDeletionReceipts(t.Context())
	got, _, err = b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "retired" {
		t.Fatal(got, err)
	}
	b.topicMu.Lock()
	n = len(b.topicDeleteReceipts)
	b.topicMu.Unlock()
	if n != 0 {
		t.Fatal("receipt not settled", n)
	}
	if len(fake.callsTo("deleteForumTopic")) != 1 || len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("repair must send one delayed notice and never repeat delete")
	}
}

func TestTopicDeletePredispatchBlockDoesNotCallTelegram(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	b.topicMu.Lock()
	b.topicUncertain = map[chatThread]bool{{streamChat, 7}: true}
	b.topicMu.Unlock()
	if err := b.deleteTopic(t.Context(), d); !errors.Is(err, ErrTopicTransportUncertain) {
		t.Fatal(err)
	}
	got, _, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "refused" {
		t.Fatal(got, err)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 {
		t.Fatal("deleted with unknown prior output")
	}
	b.topicMu.Lock()
	held := b.topicUncertain[chatThread{streamChat, 7}]
	b.topicMu.Unlock()
	if !held {
		t.Fatal("lost prior uncertainty")
	}
}

func TestTopicDeletePreflightRefusalRetriesReopen(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return sentMessage("999") }}
	b, d := topicFenceFixture(t, fake)
	_, release, err := b.topicTransport.Enter(t.Context(), strconv.FormatInt(streamChat, 10), 7)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if err = b.deleteTopic(ctx, d); err == nil {
		t.Fatal("joined transport too soon")
	}
	b.topicMu.Lock()
	held := len(b.topicDeleteReceipts)
	b.topicMu.Unlock()
	if held != 1 {
		t.Fatal("reopen work lost", held)
	}
	if err = b.RequestTopicDeletion(t.Context(), d.Scope, 7); err == nil {
		t.Fatal("replaced unsettled intent")
	}
	release()
	b.reconcileTopicDeletionReceipts(t.Context())
	b.topicMu.Lock()
	held = len(b.topicDeleteReceipts)
	b.topicMu.Unlock()
	if held != 0 {
		t.Fatal("reopen did not settle", held)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 {
		t.Fatal("preflight issued HTTP")
	}
	_, release, err = b.topicIngress.Enter(t.Context(), d.Scope.JID, 7)
	if err != nil {
		t.Fatal("refused topic did not reopen", err)
	}
	release()
}

func TestDeletedTopicCleanupRetriesTransientFailure(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreTopicFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_retire BEFORE UPDATE OF state ON topic_deletions WHEN NEW.state='retired' BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	b.recoverDeletedTopics(t.Context())
	got, _, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "deleted" {
		t.Fatal(got, err)
	}
	if _, err = b.store.DB().Exec(`DROP TRIGGER fail_retire`); err != nil {
		t.Fatal(err)
	}
	b.recoverDeletedTopics(t.Context())
	got, _, err = b.store.GetTopicDeletion(t.Context(), d.Scope, 7)
	if err != nil || got.State != "retired" {
		t.Fatal(got, err)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 || len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("cleanup replayed remote effect")
	}
}

func TestTopicDeletionReservesBoundedQueuedCapacity(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	b.topicDeleteJobs = make(chan store.TopicDeletion, topicDeleteCapacity)
	for i := 0; i < topicDeleteCapacity; i++ {
		if err := b.RequestTopicDeletion(t.Context(), d.Scope, 100+i); err != nil {
			t.Fatal(i, err)
		}
	}
	if err := b.RequestTopicDeletion(t.Context(), d.Scope, 200); err == nil {
		t.Fatal("capacity not reserved for queued jobs")
	}
	if _, found, err := b.store.GetTopicDeletion(t.Context(), d.Scope, 200); err != nil || found {
		t.Fatal("capacity refusal persisted intent", found, err)
	}
	first := <-b.topicDeleteJobs
	r := topicDeleteReceipt{intent: first, outcome: "refused", unissued: true}
	b.retainTopicDeletionReceipt(r)
	if err := b.settleTopicDeletionReceipt(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := b.RequestTopicDeletion(t.Context(), d.Scope, 200); err != nil {
		t.Fatal("settlement did not release capacity", err)
	}
}

func TestThreadInspectorReachableFromMainAfterTopicFenced(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return sentMessage("999") }}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if err := b.RestoreTopicFences(t.Context()); err != nil {
		t.Fatal(err)
	}
	b.router = control.New(nil)
	control.RegisterThreadCommands(b.router, b.store, nil)
	handler := b.topicAdmissionMiddleware(func(ctx context.Context, _ *bot.Bot, u *models.Update) {
		b.dispatchSlash(ctx, u.Message, u.Message.Text)
	})
	message := &models.Message{ID: 42, Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, From: &models.User{ID: streamChat}, Text: "/thread info"}
	handler(t.Context(), b.api, &models.Update{Message: message})
	calls := fake.callsTo("sendMessage")
	if len(calls) != 1 || !strings.Contains(calls[0].Form["text"], "Тема 7") || !strings.Contains(calls[0].Form["text"], "неизвестен") {
		t.Fatal(calls)
	}
	message.MessageThreadID = 7
	handler(t.Context(), b.api, &models.Update{Message: message})
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("fenced topic admitted inspector")
	}
}
