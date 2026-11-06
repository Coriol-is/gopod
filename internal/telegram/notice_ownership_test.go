package telegram

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

func noticeSender(kind string, b *Bot) func(context.Context) error {
	switch kind {
	case "restart":
		return func(ctx context.Context) error { return b.SendRestartNotice(ctx, streamChat, 0, "restart") }
	case "lifecycle":
		b.markThreadGone(streamChat, 7)
		return func(ctx context.Context) error { return b.sendNotice(ctx, streamChat, 7, "shutdown") }
	default:
		return func(ctx context.Context) error {
			return NewIPCSink(b).Send(ctx, buildChatJID(streamChat), strings.Repeat("x", 7000))
		}
	}
}

func TestNoticeOwnershipDurableOrigin(t *testing.T) {
	for _, kind := range []string{"restart", "lifecycle", "ipc"} {
		t.Run(kind, func(t *testing.T) {
			n := 800
			fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) { n++; return sentMessage(strconv.Itoa(n)) }}
			b, path, _ := ownershipFixture(t, fake)
			if err := noticeSender(kind, b)(t.Context()); err != nil {
				t.Fatal(err)
			}
			reopenOwnership(t, b, path)
			owner, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 801)
			want := 0
			if kind == "lifecycle" {
				want = 7
			}
			if err != nil || owner.SourceThread != want || owner.TargetThread != 0 {
				t.Fatalf("owner %+v: %v", owner, err)
			}
			if kind == "ipc" {
				if _, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 802); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestNoticeOwnershipFailureWindows(t *testing.T) {
	for _, kind := range []string{"restart", "lifecycle", "ipc"} {
		for _, window := range []string{"preinsert", "ack_failure", "unknown_remote", "canceled_ack"} {
			t.Run(kind+"/"+window, func(t *testing.T) {
				fake := &fakeTelegram{}
				b, path, scope := ownershipFixture(t, fake)
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				fake.respond = func(string, map[string]string) (int, string) {
					if window == "unknown_remote" {
						return apiError(500, "unavailable")
					}
					_ = b.store.Close()
					if window == "canceled_ack" {
						cancel()
					}
					return sentMessage("901")
				}
				if window == "preinsert" {
					_ = b.store.Close()
				}
				err := noticeSender(kind, b)(ctx)
				if err == nil || (window != "preinsert" && !noReplayError(err)) {
					t.Fatalf("lost no replay: %v", err)
				}
				if kind == "ipc" && !errors.Is(err, ipc.ErrPermanent) {
					t.Fatalf("IPC replay possible: %v", err)
				}
				calls := 1
				if window == "preinsert" {
					calls = 0
				}
				if got := len(fake.callsTo("sendMessage")); got != calls {
					t.Fatalf("calls %d want %d", got, calls)
				}
				reopenOwnership(t, b, path)
				blocked, err := b.store.UnknownMainControlBlocked(t.Context(), streamChat)
				if err != nil || blocked != (window != "preinsert") {
					t.Fatalf("durable blocker %v: %v", blocked, err)
				}
				if kind == "lifecycle" && window != "preinsert" {
					d, _, err := b.store.BeginTopicDeletion(t.Context(), scope, 7)
					if err != nil {
						t.Fatal(err)
					}
					if err := b.QuiesceTopic(t.Context(), d); !errors.Is(err, store.ErrMessageOwnership) {
						t.Fatalf("lost topic hold: %v", err)
					}
				}
				if len(fake.callsTo("sendMessage")) != calls {
					t.Fatal("recovery sent HTTP")
				}
			})
		}
	}
}

func TestNoticeOwnershipFreshGoneFallback(t *testing.T) {
	fake := &fakeTelegram{respond: func(_ string, p map[string]string) (int, string) {
		if p["message_thread_id"] == "7" {
			return apiError(400, "Bad Request: message thread not found")
		}
		return sentMessage("902")
	}}
	b, path, _ := ownershipFixture(t, fake)
	if err := b.sendNotice(t.Context(), streamChat, 7, "shutdown"); err != nil {
		t.Fatal(err)
	}
	reopenOwnership(t, b, path)
	owner, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 902)
	if err != nil || owner.SourceThread != 7 {
		t.Fatalf("owner %+v: %v", owner, err)
	}
	if len(fake.callsTo("sendMessage")) != 2 {
		t.Fatal("unexpected fallback count")
	}
}

func TestNoticeOwnershipIPCDefiniteTransientRemainsRetryable(t *testing.T) {
	fake := &fakeTelegram{respond: func(string, map[string]string) (int, string) {
		return apiError(429, "Too Many Requests: retry after 1")
	}}
	b, _, _ := ownershipFixture(t, fake)
	err := NewIPCSink(b).Send(t.Context(), buildChatJID(streamChat), "notice")
	if err == nil || errors.Is(err, ipc.ErrPermanent) || noReplayError(err) {
		t.Fatalf("definite transient changed: %v", err)
	}
	blocked, err := b.store.UnknownMainControlBlocked(t.Context(), streamChat)
	if err != nil || blocked {
		t.Fatalf("refusal left ambiguity %v: %v", blocked, err)
	}
}
