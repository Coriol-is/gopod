package telegram

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// A group /login from the owner starts nothing in the group: groups have no
// sign-in of their own (D048), so the owner gets the real step privately and
// the group a neutral line.
func (b *Bot) requestLoginHandoff(ctx context.Context, m *models.Message) bool {
	if m == nil {
		return false
	}
	b.cancelUnsafeLogin(m.Chat.ID)
	if (m.Chat.Type != models.ChatTypeGroup && m.Chat.Type != models.ChatTypeSupergroup) || m.Chat.ID >= 0 || b.ownerChatID <= 0 || userID(m.From) != b.ownerChatID || m.From.IsBot || b.api == nil {
		return false
	}
	rc, ok := b.groupLoginTarget(ctx, m.Chat.ID)
	if !ok {
		return false
	}
	b.groupLoginGuidance(ctx, b.ownerChatID, 0, rc)
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
