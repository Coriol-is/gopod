package telegram

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/queue"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

func topicFenceFixture(t *testing.T, fake *fakeTelegram) (*Bot, store.TopicDeletion) {
	t.Helper()
	b, _ := newStreamFixture(t, fake)
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	scope, err := b.store.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	d, created, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil || !created {
		t.Fatalf("begin: %v %v", created, err)
	}
	return b, d
}

func TestTopicFenceIngressAndCallbacks(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	calls := 0
	h := b.topicAdmissionMiddleware(func(context.Context, *bot.Bot, *models.Update) { calls++ })
	b.onCallback("probe", func(context.Context, cbQuery) (string, error) { calls++; return "ok", nil })
	for _, state := range []string{"deleting", "refused", "uncertain", "deleted", "retired"} {
		if state == "uncertain" {
			var err error
			d, _, err = b.store.BeginTopicDeletion(t.Context(), d.Scope, 7)
			if err != nil {
				t.Fatal(err)
			}
		}
		if state == "retired" {
			if err := b.store.RetireDeletedTopic(t.Context(), d); err != nil {
				t.Fatal(err)
			}
		} else if state != "deleting" {
			if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, state); err != nil {
				t.Fatal(err)
			}
		}
		before := calls
		m := &models.Message{Chat: models.Chat{ID: streamChat, Type: models.ChatTypePrivate}, MessageThreadID: 7}
		h(t.Context(), b.api, &models.Update{Message: m})
		toast := b.dispatchCallback(t.Context(), cbUpdate(state, streamChat, 7, 123, "probe").CallbackQuery, func(string) {})
		if state == "refused" {
			if calls != before+2 || toast != "ok" {
				t.Fatalf("refusal did not reopen: %d %q", calls-before, toast)
			}
		} else if calls != before || toast != cbToastExpired {
			t.Fatalf("%s admitted work: %d %q", state, calls-before, toast)
		}
		b.noteInboundThread(m)
		if state != "refused" && b.topicAdmission(t.Context(), streamChat, 7) == nil {
			t.Fatal("inbound erased durable fence")
		}
	}
	// Main, another topic, group and unregistered public commands stay available.
	for _, tc := range []struct {
		chat   int64
		thread int
		kind   models.ChatType
	}{{streamChat, 0, models.ChatTypePrivate}, {streamChat, 8, models.ChatTypePrivate}, {-22, 7, models.ChatTypeGroup}, {2002, 7, models.ChatTypePrivate}} {
		before := calls
		h(t.Context(), b.api, &models.Update{Message: &models.Message{Chat: models.Chat{ID: tc.chat, Type: tc.kind}, MessageThreadID: tc.thread}})
		if calls != before+1 {
			t.Fatalf("unrelated ingress blocked: %+v", tc)
		}
	}
}

func TestTopicFenceKnownIntentNeverLeaksFallbackToMain(t *testing.T) {
	for _, state := range []string{"deleting", "uncertain", "deleted", "retired"} {
		t.Run(state, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, d := topicFenceFixture(t, fake)
			if state != "deleting" {
				outcome := state
				if outcome == "retired" {
					outcome = "deleted"
				}
				if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, outcome); err != nil {
					t.Fatal(err)
				}
			}
			if state == "retired" {
				if err := b.store.RetireDeletedTopic(t.Context(), d); err != nil {
					t.Fatal(err)
				}
			}
			b.markThreadGone(streamChat, 7)
			if _, err := b.sendFormattedErr(t.Context(), streamChat, 7, 1, "private text"); err == nil {
				t.Fatal("text allowed")
			}
			if _, err := b.sendDocument(t.Context(), streamChat, 7, "private.txt", []byte("private"), ""); err == nil {
				t.Fatal("document allowed")
			}
			if err := b.sendVoiceReply(t.Context(), streamChat, 7, 1, []byte("voice")); err == nil {
				t.Fatal("voice allowed")
			}
			if err := b.sendNotice(t.Context(), streamChat, 7, "private notice"); err == nil {
				t.Fatal("notice allowed")
			}
			if err := b.sendDraft(t.Context(), streamChat, 7, 1, "draft"); err == nil {
				t.Fatal("draft allowed")
			}
			if got := b.sendPlaceholder(t.Context(), streamChat, 7, 1, nil); got != 0 {
				t.Fatal("placeholder allowed")
			}
			_, item := newStreamFixture(t, &fakeTelegram{})
			item.ThreadID = 7
			if err := b.replyInMain(t.Context(), item, "private result"); err == nil {
				t.Fatal("final fallback allowed")
			}
			b.sendErrorReply(t.Context(), item, "private error", nil)
			b.chatAction(t.Context(), streamChat, 7, models.ChatActionTyping)
			fake.mu.Lock()
			n := len(fake.calls)
			fake.mu.Unlock()
			if n != 0 {
				t.Fatalf("issued %d API requests", n)
			}
		})
	}
}

func TestTopicFenceDeletionDuringRefusedSendStopsMainFallback(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "sendMessage" {
			if form["message_thread_id"] != "7" {
				t.Error("leaked to main")
			}
			if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
				t.Error(err)
			}
			return apiError(400, "message thread not found")
		}
		return sentMessage("10")
	}
	_, err := b.sendFormattedErr(t.Context(), streamChat, 7, 1, "private")
	if !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatalf("got %v", err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("fallback request issued")
	}
}

func TestTopicFenceDeletionBetweenTextChunksStopsRemainingOutput(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
				t.Error(err)
			}
		}
		return sentMessage("10")
	}
	text := strings.Repeat("x", 8000)
	id, err := b.sendFormattedErr(t.Context(), streamChat, 7, 1, text)
	if id != 10 || !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("second chunk issued")
	}
}

func TestTopicFenceMainFallbackKeepsOriginalTopicBetweenChunks(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	b.markThreadGone(streamChat, 7)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendMessage" {
			if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
				t.Error(err)
			}
		}
		return sentMessage("10")
	}
	id, err := b.sendFormattedErr(t.Context(), streamChat, 7, 1, strings.Repeat("x", 8000))
	if id != 10 || !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatalf("id=%d err=%v", id, err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("second fallback chunk issued after deletion began")
	}
}

func TestTopicFenceStoreFailureDoesNotAdmitTopic(t *testing.T) {
	fake := &fakeTelegram{}
	b, _ := topicFenceFixture(t, fake)
	if err := b.store.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := b.sendFormattedErr(t.Context(), streamChat, 7, 1, "private"); err == nil {
		t.Fatal("store failure allowed send")
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("API called with unreadable fence")
	}
}

func TestTopicFenceFinalMainFallbackRetainsOrigin(t *testing.T) {
	for _, kind := range []string{"chunks", "code uploads", "rate limit retry"} {
		t.Run(kind, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, d := topicFenceFixture(t, fake)
			if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
				t.Fatal(err)
			}
			calls := 0
			fake.respond = func(method string, _ map[string]string) (int, string) {
				if method == "sendMessage" || method == "sendDocument" {
					calls++
					if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
						t.Error(err)
					}
					if kind == "rate limit retry" {
						return tooManyRequests("1")
					}
				}
				return sentMessage("10")
			}
			b.sleep = func(context.Context, time.Duration) error { return nil }
			text := strings.Repeat("x", 8000)
			if kind == "code uploads" {
				text = fence("go", 100) + "\n" + fence("py", 100)
			}
			if kind == "rate limit retry" {
				text = "private"
			}
			_ = b.replyInMain(t.Context(), queue.Item{ChatID: streamChat, Folder: "owner", ThreadID: 7}, text)
			if calls != 1 {
				t.Fatalf("%s: issued %d content sends after deletion began", kind, calls)
			}
		})
	}
}

func TestTopicFenceDeletionDuringUploadActionPreventsDocument(t *testing.T) {
	fake := &fakeTelegram{}
	b, d := topicFenceFixture(t, fake)
	if err := b.store.RecordTopicDeletionOutcome(t.Context(), d, "refused"); err != nil {
		t.Fatal(err)
	}
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendChatAction" {
			if _, _, err := b.store.BeginTopicDeletion(t.Context(), d.Scope, 7); err != nil {
				t.Error(err)
			}
		}
		return sentMessage("10")
	}
	_, err := b.sendDocument(t.Context(), streamChat, 7, "private.txt", []byte("private"), "")
	if !errors.Is(err, store.ErrTopicDeleting) {
		t.Fatalf("got %v", err)
	}
	if len(fake.callsTo("sendDocument")) != 0 {
		t.Fatal("document uploaded after deletion began during chat action")
	}
}
