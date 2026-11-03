package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestIPCSink_BadJIDIsPermanent(t *testing.T) {
	// A nil bot is fine — Send must reject bad JIDs before touching
	// the API.
	sink := IPCSink{bot: nil}
	err := sink.Send(context.Background(), "not-a-number", "x")
	if err == nil || !errors.Is(err, ipc.ErrPermanent) {
		t.Errorf("Send(bad jid): err = %v, want ipc.ErrPermanent", err)
	}
}

func TestIPCSink_CanonicalAndLegacyIDs(t *testing.T) {
	for _, jid := range []string{"tg:89160926", "tg:-1001234567890", "89160926"} {
		t.Run(jid, func(t *testing.T) {
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { return 200, `{"ok":true,"result":{"message_id":77}}` }}
			sink := NewIPCSink(newFakeBot(t, fake))
			text := strings.Repeat("дайджест🙂\n", 1000)
			if err := sink.Send(context.Background(), jid, text); err != nil {
				t.Fatal(err)
			}
			var joined strings.Builder
			calls := fake.callsTo("sendMessage")
			if len(calls) < 2 {
				t.Fatal("long digest was not split")
			}
			for _, call := range calls {
				if call.Form["chat_id"] != strings.TrimPrefix(jid, "tg:") {
					t.Fatalf("wrong recipient: %v", call.Form)
				}
				part := call.Form["text"]
				if len(part) > 3500 || !utf8.ValidString(part) {
					t.Fatal("invalid chunk")
				}
				joined.WriteString(part)
			}
			if joined.String() != text {
				t.Fatal("digest changed")
			}
		})
	}
}

func TestIPCSink_DeliveryErrors(t *testing.T) {
	for _, tc := range []struct {
		name               string
		code               int
		partial, permanent bool
	}{
		{"bad request", 400, false, true}, {"forbidden", 403, false, true},
		{"server error", 500, false, false}, {"partial server error", 500, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
				calls++
				if tc.partial && calls == 1 {
					return 200, `{"ok":true,"result":{"message_id":77}}`
				}
				return apiError(tc.code, "test failure")
			}}
			err := NewIPCSink(newFakeBot(t, fake)).Send(context.Background(), "tg:89160926", strings.Repeat("x", 7000))
			if err == nil || errors.Is(err, ipc.ErrPermanent) != tc.permanent {
				t.Fatalf("error = %v", err)
			}
			if tc.partial && !strings.Contains(err.Error(), "1 of 2") {
				t.Fatalf("missing partial evidence: %v", err)
			}
		})
	}
}

func ipcNoticeScope(t *testing.T, b *Bot) store.ConversationScope {
	t.Helper()
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(streamChat), 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	return conv
}

func TestIPCSinkScopedNoticePreservesTopicAndNeverFallsBack(t *testing.T) {
	for _, refuse := range []bool{false, true} {
		t.Run(fmt.Sprint(refuse), func(t *testing.T) {
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
				if refuse {
					return apiError(400, "message thread not found")
				}
				return sentMessage("10")
			}}
			b, _ := newStreamFixture(t, fake)
			conv := ipcNoticeScope(t, b)
			b.markThreadGone(streamChat, 7) // generic legacy redirect must not affect this sink
			err := NewIPCSink(b).SendConversation(t.Context(), conv, "notice")
			if (err != nil) != refuse {
				t.Fatalf("got %v", err)
			}
			calls := fake.callsTo("sendMessage")
			if len(calls) != 1 || calls[0].Form["message_thread_id"] != "7" {
				t.Fatalf("wrong destination: %+v", calls)
			}
		})
	}
}

func TestIPCSinkScopedNoticeRevalidatesBeforeEachChunk(t *testing.T) {
	fake := &fakeTelegram{}
	b, _ := newStreamFixture(t, fake)
	conv := ipcNoticeScope(t, b)
	fake.respond = func(string, map[string]string) (int, string) {
		if _, _, err := b.store.BeginTopicDeletion(t.Context(), conv.Scope, 7); err != nil {
			t.Error(err)
		}
		return sentMessage("10")
	}
	err := NewIPCSink(b).SendConversation(t.Context(), conv, strings.Repeat("x", 7000))
	if !errors.Is(err, store.ErrTopicDeleting) || !errors.Is(err, ipc.ErrPermanent) {
		t.Fatalf("got %v", err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("sent after deletion began")
	}
}

func TestIPCSinkScopedNoticeRejectsStaleScopeBeforeSend(t *testing.T) {
	fake := &fakeTelegram{}
	b, _ := newStreamFixture(t, fake)
	conv := ipcNoticeScope(t, b)
	conv.Scope.RegistrationRevision++
	err := NewIPCSink(b).SendConversation(t.Context(), conv, "private path")
	if !errors.Is(err, store.ErrStaleScope) {
		t.Fatalf("got %v", err)
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("sent stale destination")
	}
}
