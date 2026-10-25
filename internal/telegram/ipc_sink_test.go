package telegram

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/ipc"
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
