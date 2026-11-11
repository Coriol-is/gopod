package telegram

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func TestExactTopicDeletedClassifier(t *testing.T) {
	for _, tc := range []struct {
		description string
		code        int
		want        bool
	}{
		{"TOPIC_DELETED", 400, true}, {"Bad Request: TOPIC_DELETED", 400, true},
		{"Bad Request: TOPIC_CLOSED", 400, false}, {"Bad Request: message thread not found", 400, false},
		{"TOPIC_DELETED_OTHER", 400, false}, {"some TOPIC_DELETED note", 400, false}, {"TOPIC_DELETED", 500, false},
	} {
		t.Run(tc.description+fmt.Sprint(tc.code), func(t *testing.T) {
			cause := bot.ErrorBadRequest
			if tc.code != 400 {
				cause = errors.New("server error")
			}
			err := fmt.Errorf("%w, %s", cause, tc.description)
			if got := isExactTopicDeleted(err); got != tc.want {
				t.Fatalf("%v: %v", err, got)
			}
			if isExactTopicDeleted(&ownershipSendError{cause: err}) {
				t.Fatal("wrapped unproven error accepted")
			}
		})
	}
}

func TestObservedTopicDeletionGatedAndNeverFallsBack(t *testing.T) {
	for _, tc := range []struct {
		name, description string
		enabled, other    bool
		deleted           bool
	}{
		{"exact", "Bad Request: TOPIC_DELETED", true, false, true},
		{"gate-off", "Bad Request: TOPIC_DELETED", false, false, false},
		{"non-owner", "Bad Request: TOPIC_DELETED", true, true, false},
		{"closed", "Bad Request: TOPIC_CLOSED", true, false, false},
		{"not-found", "Bad Request: message thread not found", true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeTelegram{respond: func(method string, p map[string]string) (int, string) {
				if method == "sendMessage" && p["message_thread_id"] == "7" {
					return apiError(400, tc.description)
				}
				return sentMessage("999")
			}}
			b, _, ref := ownershipFixture(t, fake)
			b.topicDeletionEnabled = tc.enabled
			if tc.other {
				b.ownerChatID = streamChat + 1
			}
			if err := b.store.PutSession(t.Context(), "owner", 7, "claude", "native"); err != nil {
				t.Fatal(err)
			}
			_, err := b.sendFinal(t.Context(), streamChat, 7, 0, "test", nil)
			d, found, e := b.store.GetTopicDeletion(t.Context(), ref, 7)
			if e != nil || found != tc.deleted {
				t.Fatal(d, found, e)
			}
			if tc.deleted {
				if !noReplayError(err) || d.State != "deleted" {
					t.Fatal(d, err)
				}
				if calls := fake.callsTo("sendMessage"); len(calls) != 1 {
					t.Fatal("fallback sent", calls)
				}
				b.recoverDeletedTopics(t.Context())
				d, _, e = b.store.GetTopicDeletion(t.Context(), ref, 7)
				if e != nil || d.State != "retired" {
					t.Fatal(d, e)
				}
				if len(fake.callsTo("deleteForumTopic")) != 0 {
					t.Fatal("external deletion replayed")
				}
				if e = b.topicAdmission(t.Context(), streamChat, 8); e != nil {
					t.Fatal("sibling blocked", e)
				}
			} else if noReplayError(err) {
				t.Fatal("legacy compatibility changed", err)
			}
		})
	}
}

func TestObservedTopicDeletionPersistenceFailureHolds(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, p map[string]string) (int, string) {
		if method == "sendMessage" {
			return apiError(400, "Bad Request: TOPIC_DELETED")
		}
		return sentMessage("999")
	}}
	b, _, ref := ownershipFixture(t, fake)
	b.topicDeletionEnabled = true
	// A stale captured registration must not retire a newly registered scope.
	fake.respond = func(method string, p map[string]string) (int, string) {
		if method == "sendMessage" {
			if e := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", Name: "changed", IsOwner: true, AddedAt: 1}); e != nil {
				t.Fatal(e)
			}
			return apiError(400, "Bad Request: TOPIC_DELETED")
		}
		return sentMessage("999")
	}
	_, err := b.sendFinal(t.Context(), streamChat, 7, 0, "test", nil)
	if !noReplayError(err) || !errors.Is(err, store.ErrStaleScope) {
		t.Fatal(err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("stale observation replayed")
	}
	if _, _, err = b.store.GetTopicDeletion(t.Context(), ref, 7); !errors.Is(err, store.ErrStaleScope) {
		t.Fatal(err)
	}
	pending, e := b.store.PendingTopicDeletions(t.Context())
	if e != nil || len(pending) != 0 {
		t.Fatal(pending, e)
	}
	if !strings.Contains(fmt.Sprint(err), "stale") {
		t.Fatal(err)
	}
}

func TestObservedDeletionWaitsForExistingIngressAndRestoresFence(t *testing.T) {
	fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			return apiError(400, "Bad Request: TOPIC_DELETED")
		}
		return sentMessage("999")
	}}
	b, path, ref := ownershipFixture(t, fake)
	b.topicDeletionEnabled = true
	if e := b.store.PutSession(t.Context(), "owner", 7, "claude", "native"); e != nil {
		t.Fatal(e)
	}
	_, release, e := b.topicIngress.Enter(t.Context(), ref.JID, 7)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = b.sendFinal(t.Context(), streamChat, 7, 0, "test", nil); !noReplayError(e) {
		t.Fatal(e)
	}
	b.recoverDeletedTopics(t.Context())
	if _, ok, e := b.store.GetSession(t.Context(), "owner", 7, "claude"); e != nil || !ok {
		t.Fatal("retired before ingress joined", ok, e)
	}
	release()
	if e = b.store.Close(); e != nil {
		t.Fatal(e)
	}
	next := newFakeBot(t, fake)
	next.store, e = store.Open(t.Context(), path, nil)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = next.store.Close() })
	if e = next.RestoreTopicFences(t.Context()); e != nil {
		t.Fatal(e)
	}
	next.recoverDeletedTopics(t.Context())
	d, _, e := next.store.GetTopicDeletion(t.Context(), ref, 7)
	if e != nil || d.State != "retired" {
		t.Fatal(d, e)
	}
	if len(fake.callsTo("deleteForumTopic")) != 0 || len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("recovery replayed remote effects")
	}
}

func TestObservedDeletionSupersedesQueuedAndRefusedCommand(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(fmt.Sprint(refused), func(t *testing.T) {
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return sentMessage("999") }}
			b, _, ref := ownershipFixture(t, fake)
			owner, e := b.store.BeginTelegramMessageOwner(t.Context(), streamChat, 7, 7)
			if e != nil {
				t.Fatal(e)
			}
			b.topicDeleteJobs = make(chan store.TopicDeletion, 1)
			if e = b.RequestTopicDeletion(t.Context(), ref, 7); e != nil {
				t.Fatal(e)
			}
			d := <-b.topicDeleteJobs
			if _, e = b.installTopicFence(d, false); e != nil {
				t.Fatal(e)
			}
			if refused {
				if e = b.store.RefuseUnissuedTopicDeletion(t.Context(), d); e != nil {
					t.Fatal(e)
				}
				b.retainTopicDeletionReceipt(topicDeleteReceipt{intent: d, outcome: "refused", unissued: true, notice: "old refusal"})
			}
			observed, e := b.store.ObserveDeletedTelegramTopic(t.Context(), owner)
			if e != nil || observed.IntentID != d.IntentID {
				t.Fatal(observed, e)
			}
			if refused {
				b.reconcileTopicDeletionReceipts(t.Context())
			} else if e = b.deleteTopic(t.Context(), d); e != nil {
				t.Fatal(e)
			}
			latest, _, e := b.store.GetTopicDeletion(t.Context(), ref, 7)
			if e != nil || latest.State != "retired" {
				t.Fatal(latest, e)
			}
			if b.topicDeleteAllocated != 0 || len(b.topicDeleteReceipts) != 0 || len(b.topicDeleteReservations) != 0 {
				t.Fatal("obsolete receipt capacity retained", b.topicDeleteAllocated, b.topicDeleteReceipts)
			}
			if len(fake.callsTo("deleteForumTopic")) != 0 {
				t.Fatal("observed deletion dispatched")
			}
			calls := fake.callsTo("sendMessage")
			if len(calls) != 1 || strings.Contains(calls[0].Form["text"], "old refusal") {
				t.Fatal(calls)
			}
			b.reconcileTopicDeletionReceipts(t.Context())
			b.recoverDeletedTopics(t.Context())
			if len(fake.callsTo("sendMessage")) != 1 {
				t.Fatal("notice replayed")
			}
		})
	}
}
