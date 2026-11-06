package telegram

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

func TestTopicTargetOwnershipFailureSurvivesRestart(t *testing.T) {
	for _, window := range []string{"unknown", "ack_failure", "missing_receipt"} {
		t.Run(window, func(t *testing.T) {
			fake := &fakeTelegram{}
			b, path, scope := ownershipFixture(t, fake)
			fake.respond = func(string, map[string]string) (int, string) {
				switch window {
				case "unknown":
					return apiError(500, "message thread not found") // text must not imply definite refusal
				case "ack_failure":
					_ = b.store.Close()
					return sentMessage("810")
				default:
					return 200, `{"ok":true,"result":{}}`
				}
			}
			err := b.sendNotice(t.Context(), streamChat, 7, "private topic notice")
			if !noReplayError(err) || len(fake.callsTo("sendMessage")) != 1 {
				t.Fatalf("replayed ambiguous topic: %v", err)
			}
			reopenOwnership(t, b, path)
			n := 810
			fake.respond = func(string, map[string]string) (int, string) { n++; return sentMessage(strconv.Itoa(n)) }
			// An unresolved operation is a quiescence blocker, not a global output ban.
			for _, thread := range []int{7, 8, 0} {
				if err := b.sendNotice(t.Context(), streamChat, thread, "independent reply"); err != nil {
					t.Fatalf("thread %d blocked: %v", thread, err)
				}
			}
			sibling, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 8)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.QuiesceTopic(t.Context(), sibling); err != nil {
				t.Fatalf("sibling held: %v", err)
			}
			d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.QuiesceTopic(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
				t.Fatalf("lost restart hold: %v", err)
			}
			if len(fake.callsTo("sendMessage")) != 4 {
				t.Fatal("reconciliation invoked Telegram")
			}
		})
	}
}

func TestTopicTargetOwnershipPositiveReceiptRepair(t *testing.T) {
	fake := &fakeTelegram{}
	b, path, scope := ownershipFixture(t, fake)
	fake.respond = func(string, map[string]string) (int, string) { _ = b.store.Close(); return sentMessage("820") }
	sent, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, MessageThreadID: 7, Text: "topic"})
	var ownershipErr *ownershipSendError
	if sent == nil || sent.ID != 820 || !errors.As(err, &ownershipErr) || !ownershipErr.receiptBacked {
		t.Fatalf("lost receipt: %v %v", sent, err)
	}
	reconnectReceiptStore(t, b, path)
	d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err := b.QuiesceTopic(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	reopenOwnership(t, b, path)
	owner, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 820)
	if err != nil || owner.SourceThread != 7 || owner.TargetThread != 7 {
		t.Fatalf("owner %+v: %v", owner, err)
	}
	if len(fake.callsTo("sendMessage")) != 1 {
		t.Fatal("repair resent HTTP")
	}
}

func TestTopicTargetOwnershipDefiniteFallbackAndRetry(t *testing.T) {
	for _, code := range []int{400, 429} {
		t.Run(strconv.Itoa(code), func(t *testing.T) {
			fake := &fakeTelegram{respond: func(_ string, p map[string]string) (int, string) {
				if p["message_thread_id"] == "7" {
					return apiError(code, "Bad Request: message thread not found")
				}
				return sentMessage("830")
			}}
			b, path, scope := ownershipFixture(t, fake)
			// For 429 exercise the low-level refusal; lifecycle's text-based gone handler
			// is a separate legacy policy. A refused attempt must not remain pending.
			if code == 429 {
				_, err := b.ownedTextSend(t.Context(), streamChat, 7, &bot.SendMessageParams{ChatID: streamChat, MessageThreadID: 7, Text: "topic"})
				if err == nil || noReplayError(err) {
					t.Fatalf("refusal not retryable: %v", err)
				}
			} else if err := b.sendNotice(t.Context(), streamChat, 7, "topic"); err != nil {
				t.Fatal(err)
			}
			reopenOwnership(t, b, path)
			if code == 400 {
				owner, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 830)
				if err != nil || owner.SourceThread != 7 || owner.TargetThread != 0 {
					t.Fatalf("fallback %+v: %v", owner, err)
				}
			}
			d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
			if err != nil {
				t.Fatal(err)
			}
			if err := b.QuiesceTopic(t.Context(), d); err != nil {
				t.Fatalf("refused attempt retained: %v", err)
			}
		})
	}
}

func TestTopicTargetOwnershipRejectsForeignTargetBeforeHTTP(t *testing.T) {
	fake := &fakeTelegram{}
	b, _, _ := ownershipFixture(t, fake)
	for _, route := range [][2]int{{7, 8}, {0, 7}} {
		_, err := b.ownedTextSend(context.Background(), streamChat, route[0], &bot.SendMessageParams{ChatID: streamChat, MessageThreadID: route[1], Text: "topic"})
		if !errors.Is(err, store.ErrInvalidConversation) {
			t.Fatalf("route %v: %v", route, err)
		}
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("foreign target dispatched")
	}
}
