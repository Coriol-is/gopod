package control

import (
	"context"
	"errors"
	"html"
	"strings"
	"testing"
	"time"
)

func telegramTestRouter(t *testing.T) (*Router, *int, *int) {
	t.Helper()
	r := New(nil)
	RegisterBuiltins(r)
	reads, refreshes := new(int), new(int)
	snapshot := TelegramSnapshot{Settings: map[string]string{"has_topics_enabled": "enabled", "supports_inline_queries": "enabled"}, Freshness: "fresh", ErrorClass: "none", RefreshState: "idle", Runtime: "configured", AllowedUpdates: []string{"message", "callback_query", "stopped_message_generation"}}
	RegisterTelegramCommands(r, func() TelegramSnapshot { *reads++; return snapshot }, func(context.Context) TelegramSnapshot { *refreshes++; return snapshot }, func(_ context.Context, id int64) bool { return id == 20 })
	return r, reads, refreshes
}

func TestTelegramAuthorizationAndNestedDispatch(t *testing.T) {
	for _, tt := range []struct {
		name             string
		caller           Caller
		command          string
		args             []string
		denied           bool
		reads, refreshes int
		text             string
	}{
		{"registered summary", Caller{ChatID: 20, Source: SourceTelegram, ChatKind: "private"}, "telegram", nil, false, 1, 0, "availability"},
		{"unregistered summary", Caller{ChatID: 30, Source: SourceTelegram, ChatKind: "private"}, "telegram", nil, false, 0, 0, "not registered"},
		{"owner first message", Caller{ChatID: 10, IsOwner: true, Source: SourceTelegram, ChatKind: "private"}, "telegram", []string{"doctor"}, false, 1, 0, "Last success"},
		{"owner invalid refresh", Caller{ChatID: 10, IsOwner: true, Source: SourceTelegram, ChatKind: "private"}, "telegram", []string{"doctor", "refresh", "extra"}, false, 0, 0, "Use /telegram"},
		{"owner refresh", Caller{ChatID: 10, IsOwner: true, Source: SourceTelegram, ChatKind: "private"}, "telegram", []string{"doctor", "refresh"}, false, 0, 1, "Last success"},
		{"nonowner nested doctor", Caller{ChatID: 20, Source: SourceTelegram, ChatKind: "private"}, "telegram", []string{"doctor", "refresh"}, true, 0, 0, "Not authorized"},
		{"nonowner canonical doctor", Caller{ChatID: 20, Source: SourceTelegram, ChatKind: "private"}, "telegram.doctor", []string{"refresh"}, true, 0, 0, "Not authorized"},
		{"group owner", Caller{ChatID: 10, IsOwner: true, Source: SourceTelegram, ChatKind: "supergroup"}, "telegram", []string{"doctor", "refresh"}, false, 0, 0, ""},
		{"unknown kind", Caller{ChatID: 10, IsOwner: true, Source: SourceTelegram}, "telegram.doctor", []string{"refresh"}, false, 0, 0, ""},
		{"CLI", Caller{IsOwner: true, Source: SourceCLI, ChatKind: "private"}, "telegram.doctor", []string{"refresh"}, false, 0, 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r, reads, refreshes := telegramTestRouter(t)
			resp, err := r.Dispatch(context.Background(), Command{Name: tt.command, Args: tt.args, Caller: tt.caller})
			if errors.Is(err, ErrNotAuthorized) != tt.denied {
				t.Fatalf("error=%v", err)
			}
			if *reads != tt.reads || *refreshes != tt.refreshes {
				t.Fatalf("reads=%d refreshes=%d", *reads, *refreshes)
			}
			if tt.text == "" && resp.Text != "" {
				t.Fatalf("unexpected output %q", resp.Text)
			}
			if !strings.Contains(resp.Text, tt.text) {
				t.Fatalf("response=%q", resp.Text)
			}
			if resp.Data != nil || len(resp.Buttons) > 0 {
				t.Fatal("unexpected payload/buttons")
			}
			if !tt.caller.IsOwner && strings.Contains(resp.Text, "Last success") {
				t.Fatal("owner report leaked")
			}
		})
	}
}

func TestTelegramHelpIsPrivateAndDeterministic(t *testing.T) {
	r, reads, refreshes := telegramTestRouter(t)
	for _, kind := range []string{"private", "group", "supergroup", "channel", ""} {
		caller := Caller{Source: SourceTelegram, ChatKind: kind}
		for _, topic := range []string{"guest", "inline", "topics"} {
			resp, err := r.Dispatch(context.Background(), Command{Name: "help", Args: []string{topic}, Caller: caller})
			if err != nil {
				t.Fatal(err)
			}
			if (resp.Text != "") != (kind == "private") {
				t.Fatalf("kind=%s topic=%s response=%q", kind, topic, resp.Text)
			}
		}
		resp, _ := r.Dispatch(context.Background(), Command{Name: "help", Caller: caller})
		if strings.Contains(resp.Text, "/telegram") != (kind == "private") {
			t.Fatalf("menu kind=%s response=%q", kind, resp.Text)
		}
	}
	if *reads != 0 || *refreshes != 0 {
		t.Fatal("help queried observations")
	}
}

func TestTelegramDoctorBoundedAndSanitized(t *testing.T) {
	secret := strings.Repeat("<secret&>", 10000)
	s := TelegramSnapshot{Username: secret, Settings: map[string]string{"has_topics_enabled": secret, secret: secret}, Freshness: secret, ErrorClass: secret, RefreshState: secret, Runtime: secret, AllowedUpdates: []string{secret}, CheckedAt: time.Now(), LastAttempt: time.Now(), NextRefresh: time.Now()}
	for _, text := range []string{telegramSummary(s), telegramDoctor(s)} {
		if strings.Contains(text, "secret") {
			t.Fatal("untrusted field rendered")
		}
		// Reports contain no Markdown constructs needing expansion; HTML escaping
		// is a conservative rendered-byte bound for their fixed plain-text copy.
		if n := len(html.EscapeString(text)); n >= 3800 {
			t.Fatalf("rendered upper bound=%d", n)
		}
	}
}

func TestTelegramEffectiveReasonsAccumulate(t *testing.T) {
	s := TelegramSnapshot{Settings: map[string]string{"supports_inline_queries": "enabled"}, Freshness: "stale", Runtime: "absent"}
	reasons := strings.Join(telegramReasons(s, "supports_inline_queries", "unsupported", "inline_query"), ",")
	for _, want := range []string{"code_unsupported", "not_subscribed", "observation_stale_or_unknown", "runtime_unavailable_or_unknown", "context_rights_unknown"} {
		if !strings.Contains(reasons, want) {
			t.Errorf("missing %s in %s", want, reasons)
		}
	}
	if strings.Contains(reasons, "telegram_disabled") {
		t.Fatal("enabled setting marked disabled")
	}
	s.Settings["supports_inline_queries"] = "disabled"
	if !strings.Contains(strings.Join(telegramReasons(s, "supports_inline_queries", "ready", "inline_query"), ","), "telegram_disabled") {
		t.Fatal("disabled missing")
	}
	delete(s.Settings, "supports_inline_queries")
	if !strings.Contains(strings.Join(telegramReasons(s, "supports_inline_queries", "ready", "inline_query"), ","), "telegram_unknown") {
		t.Fatal("unknown missing")
	}
}
