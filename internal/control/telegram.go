package control

import (
	"context"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// TelegramSnapshot is a copied, secret-free observation, not an execution gate.
type TelegramSnapshot struct {
	Settings                            map[string]string
	Username                            string
	CheckedAt, LastAttempt, NextRefresh time.Time
	Freshness, ErrorClass, RefreshState string
	AllowedUpdates                      []string
	Runtime                             string
	Startup                             *TelegramRuntimeObservation
}

// TelegramRuntimeObservation contains only bounded local startup evidence.
// A successful snapshot does not imply the container is still alive now.
type TelegramRuntimeObservation struct {
	CheckedAt                                  time.Time
	Provider, CLIVersion, Container, CLI, Auth string
}

// RegisterTelegramCommands keeps privileged refresh behind a separate Router
// permission check. The callbacks only read observations; they never run agents.
func RegisterTelegramCommands(r *Router, read func() TelegramSnapshot, refresh func(context.Context) TelegramSnapshot, registered func(context.Context, int64) bool) {
	r.Register("telegram", "telegram", "show Telegram mode availability (private chat)", PermChatLocal, func(ctx context.Context, cmd Command) (Response, error) {
		if !telegramPrivate(cmd.Caller) {
			return Response{}, nil
		}
		if len(cmd.Args) > 0 && cmd.Args[0] == "doctor" {
			cmd.Name = "telegram.doctor"
			cmd.Args = cmd.Args[1:]
			return r.Dispatch(ctx, cmd)
		}
		if !cmd.Caller.IsOwner && (registered == nil || !registered(ctx, cmd.Caller.ChatID)) {
			return Response{Text: "Chat not registered.", Code: 1}, nil
		}
		if len(cmd.Args) > 0 {
			return telegramUsage(), nil
		}
		s := TelegramSnapshot{}
		if read != nil {
			s = read()
		}
		return Response{Text: telegramSummary(s)}, nil
	})
	r.Register("telegram.doctor", "", "owner Telegram diagnostics", PermOwnerOnly, func(ctx context.Context, cmd Command) (Response, error) {
		if !telegramPrivate(cmd.Caller) {
			return Response{}, nil
		}
		if len(cmd.Args) > 1 || (len(cmd.Args) == 1 && cmd.Args[0] != "refresh") {
			return telegramUsage(), nil
		}
		s := TelegramSnapshot{}
		if len(cmd.Args) == 1 && refresh != nil {
			s = refresh(ctx)
		} else if read != nil {
			s = read()
		}
		return Response{Text: telegramDoctor(s)}, nil
	})
}

func telegramPrivate(c Caller) bool { return c.Source == SourceTelegram && c.ChatKind == "private" }
func telegramUsage() Response {
	return Response{Text: "Use /telegram, /telegram doctor or /telegram doctor refresh in private chat.", Code: 1}
}
func telegramSetting(s TelegramSnapshot, key string) string {
	return telegramEnum(s.Settings[key], "enabled", "disabled", "unknown")
}
func telegramEnum(value string, allowed ...string) string {
	if slices.Contains(allowed, value) {
		return value
	}
	return "unknown"
}
func telegramErrorClass(value string) string {
	if value == "" {
		return "none"
	}
	return telegramEnum(value, "none", "timeout", "transport", "authentication", "api_rejection", "rate_limit", "schema", "canceled")
}

func telegramTime(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.UTC().Format(time.RFC3339)
}

func telegramSummary(s TelegramSnapshot) string {
	return fmt.Sprintf("Telegram availability\nObservation: %s (getMe).\nPrivate topics: %s; local support partial.\nInline: %s; local handler unsupported.\nGuest: %s; local handler unsupported.\nBusiness: %s; local handler unsupported.\nSettings do not prove an interaction is supported.\nUse /help guest, /help inline or /help topics in private chat.", telegramEnum(s.Freshness, "fresh", "stale", "unknown"), telegramSetting(s, "has_topics_enabled"), telegramSetting(s, "supports_inline_queries"), telegramSetting(s, "supports_guest_queries"), telegramSetting(s, "can_connect_to_business"))
}

// Only fixed labels and allowlisted states enter reports. Neither response size
// nor sensitive output can be controlled by raw API error strings or map keys.
func telegramDoctor(s TelegramSnapshot) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Telegram doctor (read-only)\nSource: getMe; freshness: %s\nLast success: %s\nLast attempt: %s\nRefresh permitted after: %s\nRefresh: `%s`; error: `%s`\nRuntime: %s\n", telegramEnum(s.Freshness, "fresh", "stale", "unknown"), telegramTime(s.CheckedAt), telegramTime(s.LastAttempt), telegramTime(s.NextRefresh), telegramEnum(s.RefreshState, "idle", "in_flight", "cooldown", "backoff", "stopped"), telegramErrorClass(s.ErrorClass), telegramEnum(s.Runtime, "configured", "absent", "unknown"))
	writeTelegramRuntime(&b, s.Startup)
	b.WriteString("Policy: owner private chat; group rights/business connection unknown.\n")
	for _, f := range []struct{ label, key string }{
		{"Private topics", "has_topics_enabled"}, {"User topic creation/deletion", "allows_users_to_create_topics"}, {"Group invitation", "can_join_groups"}, {"Read all group messages", "can_read_all_group_messages"}, {"Inline", "supports_inline_queries"}, {"Guest", "supports_guest_queries"}, {"Business", "can_connect_to_business"},
	} {
		fmt.Fprintf(&b, "%s: %s\n", f.label, telegramSetting(s, f.key))
	}
	if !s.CheckedAt.IsZero() {
		age := time.Since(s.CheckedAt)
		if age < 0 {
			age = 0
		}
		fmt.Fprintf(&b, "Observation age: %s\n", age.Truncate(time.Second))
	}
	b.WriteString("Bot-to-bot: unknown; no authoritative flag inspected.\nRequested updates:")
	for _, update := range []string{"message", "callback_query", "stopped_message_generation", "inline_query", "guest_message", "business_message"} {
		if slices.Contains(s.AllowedUpdates, update) {
			b.WriteString(" `" + update + "`")
		}
	}
	b.WriteString("\n")
	for _, m := range []struct{ name, setting, implementation, update string }{
		{"Private topics", "has_topics_enabled", "partial", "message"},
		{"Inline", "supports_inline_queries", "unsupported", "inline_query"},
		{"Guest", "supports_guest_queries", "unsupported", "guest_message"},
		{"Business", "can_connect_to_business", "unsupported", "business_message"},
	} {
		reasons := telegramReasons(s, m.setting, m.implementation, m.update)
		required, subscribed := m.update, "unknown"
		if required == "" {
			required = "unknown"
		} else if slices.Contains(s.AllowedUpdates, m.update) {
			subscribed = "yes"
		} else {
			subscribed = "no"
		}
		fmt.Fprintf(&b, "%s: code=%s; needs=`%s`; subscribed=%s; reasons=`%s`\n", m.name, m.implementation, required, subscribed, strings.Join(reasons, ","))
	}
	b.WriteString("Group forum rights are not private-topic settings. Invitation/privacy flags do not prove membership/admin rights. Business enablement does not prove connection/`can_reply`.\nRefresh is manual only: /telegram doctor refresh. No automatic refresh timer; settings/subscriptions unchanged.")
	return b.String()
}

var telegramVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)

func writeTelegramRuntime(b *strings.Builder, observation *TelegramRuntimeObservation) {
	if observation == nil || observation.CheckedAt.IsZero() {
		b.WriteString("Startup snapshot: unavailable; current health unverified.\n")
		return
	}
	age := time.Since(observation.CheckedAt)
	if age < 0 {
		age = 0
	}
	freshness := "historical"
	if age >= 15*time.Minute {
		freshness = "stale"
	}
	version := "unknown"
	if len(observation.CLIVersion) <= 32 && telegramVersion.MatchString(observation.CLIVersion) {
		version = observation.CLIVersion
	}
	fmt.Fprintf(b, "Startup snapshot: %s; checked %s; age %s. Current health unverified.\n", freshness, telegramTime(observation.CheckedAt), age.Truncate(time.Second))
	fmt.Fprintf(b, "Startup: provider=%s; container=`%s`; CLI=`%s`; version=%s; auth=`%s`\n", telegramEnum(observation.Provider, "claude", "codex"), telegramEnum(observation.Container, "ready", "error", "canceled", "timeout", "selection_unavailable"), telegramEnum(observation.CLI, "ready", "error", "canceled", "timeout", "cleanup_failed"), version, telegramEnum(observation.Auth, "authenticated", "not_authenticated", "error", "canceled", "timeout", "cleanup_failed"))
}

func telegramReasons(s TelegramSnapshot, setting, implementation, update string) []string {
	reasons := []string{}
	if implementation != "ready" {
		reasons = append(reasons, "code_"+implementation)
	}
	switch telegramSetting(s, setting) {
	case "disabled":
		reasons = append(reasons, "telegram_disabled")
	case "unknown":
		reasons = append(reasons, "telegram_unknown")
	}
	if update == "" {
		reasons = append(reasons, "required_update_unknown")
	} else if !slices.Contains(s.AllowedUpdates, update) {
		reasons = append(reasons, "not_subscribed")
	}
	if s.Freshness != "fresh" {
		reasons = append(reasons, "observation_stale_or_unknown")
	}
	if s.Runtime != "configured" {
		reasons = append(reasons, "runtime_unavailable_or_unknown")
	}
	reasons = append(reasons, "context_rights_unknown")
	return reasons
}

func telegramHelpTopic(topic string) (string, bool) {
	switch topic {
	case "guest":
		return "Guest mode: a BotFather setting does not install a local handler. gopod does not process Guest queries or inherit owner memory/credentials for them. If Telegram does not deliver a disabled mode, the bot cannot answer there. In this bot's private chat, use /telegram for availability.", true
	case "inline":
		return "Inline mode: BotFather enablement and local support are separate. gopod has no inline-query handler or subscription. Enabling inline alone will not make it work. Disabled inline requests cannot be intercepted by the bot. In private chat, use /telegram for availability.", true
	case "topics":
		return "Topics: private-chat topics and group forum topics have different settings and rights. gopod preserves private-topic reply routing, but provider selection remains chat-wide. Topic deletion/session retirement is not implemented; no deleted-topic event is assumed. In private chat, use /telegram for availability.", true
	}
	return "", false
}
