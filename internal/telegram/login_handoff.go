package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

const groupLoginRedirectCooldown = 30 * time.Second

// A group /login from the owner starts no sign-in: groups have none of their
// own (D048, D049), so the owner gets the real step privately and the group one
// neutral line. A command addressed to another bot is ignored, and repeats in
// the same group are rate limited so the group is not spammed.
func (b *Bot) requestLoginHandoff(ctx context.Context, m *models.Message) bool {
	if m == nil {
		return false
	}
	b.cancelUnsafeLogin(m.Chat.ID)
	if (m.Chat.Type != models.ChatTypeGroup && m.Chat.Type != models.ChatTypeSupergroup) || m.Chat.ID >= 0 || b.ownerChatID <= 0 || userID(m.From) != b.ownerChatID || m.From.IsBot || b.api == nil {
		return false
	}
	if target := commandTarget(strings.TrimSpace(messageText(m))); target != "" && !strings.EqualFold(target, b.botUsername()) {
		return false
	}
	rc, ok := b.groupLoginTarget(ctx, m.Chat.ID)
	if !ok || !b.allowLoginRedirect(m.Chat.ID) {
		return false
	}
	b.groupLoginGuidance(ctx, b.ownerChatID, 0, rc)
	return true
}

// allowLoginRedirect admits one redirect per group per cooldown; the map is
// pruned on every call, so it holds only groups seen within the cooldown.
func (b *Bot) allowLoginRedirect(chatID int64) bool {
	now := b.clock()
	b.loginRedirectMu.Lock()
	defer b.loginRedirectMu.Unlock()
	if b.loginRedirectLast == nil {
		b.loginRedirectLast = make(map[int64]time.Time)
	}
	for chat, last := range b.loginRedirectLast {
		if now.Sub(last) >= groupLoginRedirectCooldown {
			delete(b.loginRedirectLast, chat)
		}
	}
	if _, recent := b.loginRedirectLast[chatID]; recent {
		return false
	}
	b.loginRedirectLast[chatID] = now
	return true
}

func (b *Bot) groupLoginTarget(ctx context.Context, chatID int64) (store.RegisteredChat, bool) {
	if b.store == nil || chatID >= 0 {
		return store.RegisteredChat{}, false
	}
	target, err := b.store.GetRegistered(ctx, buildChatJID(chatID))
	if err != nil || target.IsOwner || target.Folder == "owner" {
		return store.RegisteredChat{}, false
	}
	owner, err := b.store.GetRegistered(ctx, buildChatJID(b.ownerChatID))
	if err == nil && owner.Folder == target.Folder {
		return store.RegisteredChat{}, false
	}
	if err != nil && !errors.Is(err, store.ErrChatNotRegistered) {
		return store.RegisteredChat{}, false
	}
	return target, true
}

// Credential-bearing login messages never use generic send fallbacks or raw
// transport error logs. Every caller already established a private recipient.
func (b *Bot) sendPrivateLoginText(ctx context.Context, chatID int64, thread int, text string, markup models.ReplyMarkup) (int, error) {
	if chatID <= 0 || b.api == nil {
		return 0, errors.New("private login delivery unavailable")
	}
	sendCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sent, err := b.api.SendMessage(sendCtx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: thread, Text: markdownToTelegramHTML(text), ParseMode: models.ParseModeHTML, ReplyMarkup: markup})
	if err != nil || sent == nil || sent.ID == 0 {
		return 0, errors.New("private login delivery failed")
	}
	return sent.ID, nil
}
func (b *Bot) replyLoginText(ctx context.Context, chatID int64, thread int, text string) {
	if _, err := b.sendPrivateLoginText(ctx, chatID, thread, text, nil); err != nil {
		b.log.Warn("private login message not delivered", slog.String("error_class", "private_delivery_failed"))
	}
}
