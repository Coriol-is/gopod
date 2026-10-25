package telegram

import (
	"context"
	"strings"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/go-telegram/bot/models"
)

// diagnosticCommand recognizes the new routes even when their arguments or
// suffix are invalid, so they cannot fall through into public error rendering.
func (b *Bot) diagnosticCommand(m *models.Message, text string) (bool, bool) {
	parts := strings.Fields(text)
	if len(parts) == 0 || !strings.HasPrefix(parts[0], "/") {
		return false, false
	}
	name, suffix, qualified := strings.Cut(strings.TrimPrefix(parts[0], "/"), "@")
	diagnostic := name == "telegram" || name == "help" && len(parts) > 1 && (parts[1] == "guest" || parts[1] == "inline" || parts[1] == "topics")
	if !diagnostic {
		return false, false
	}
	if m == nil || m.Chat.Type != models.ChatTypePrivate {
		return true, false
	}
	if qualified {
		username := b.TelegramSnapshot().Username
		if username == "" || suffix == "" || !strings.EqualFold(suffix, username) {
			return true, false
		}
	}
	return true, true
}

func (b *Bot) dispatchRawTelegramDiagnostics(ctx context.Context, m *models.Message, text string) bool {
	diagnostic, accepted := b.diagnosticCommand(m, text)
	if !diagnostic {
		return false
	}
	if accepted {
		if b.ownerChatID != 0 && m.Chat.ID == b.ownerChatID && b.store != nil {
			if _, ok := b.resolveRegistered(ctx, m); !ok {
				b.sendDiagnosticResponse(ctx, m, "Telegram diagnostics unavailable: chat registration failed.")
				return true
			}
		}
		b.dispatchSlash(ctx, m, text)
	}
	return true
}

// Send a single bounded message, bypassing generic command pagination/buttons.
func (b *Bot) sendDiagnosticResponse(ctx context.Context, m *models.Message, text string) {
	if len(markdownToTelegramHTML(text)) > pageMaxHTMLBytes {
		text = "Telegram diagnostic report unavailable: output exceeds the safe message limit."
	}
	b.replyTo(ctx, m.Chat.ID, inboundThreadID(m), m.ID, text)
}

func (b *Bot) TelegramSnapshot() control.TelegramSnapshot {
	var s control.TelegramSnapshot
	if b.capabilities != nil {
		s = b.capabilities.Snapshot()
	}
	s.AllowedUpdates = make([]string, len(allowedUpdates))
	for i, u := range allowedUpdates {
		s.AllowedUpdates[i] = string(u)
	}
	s.Runtime = "absent"
	if b.runner != nil {
		s.Runtime = "configured"
	}
	return s
}

func (b *Bot) RefreshTelegramSnapshot(ctx context.Context) control.TelegramSnapshot {
	if b.capabilities == nil {
		return b.TelegramSnapshot()
	}
	b.capabilities.Refresh(ctx)
	return b.TelegramSnapshot()
}
