package telegram

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/spaceinvaderz/picoclaw/internal/store"
)

// defaultHandler is invoked by go-telegram/bot for every update that no
// registered handler matched. We persist the message and otherwise stay
// silent — M1 has no agent loop yet.
func (b *Bot) defaultHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	b.persistMessage(ctx, update.Message)
}

// pingHandler answers /ping with "pong" and persists the inbound /ping
// message itself (registered handlers do not fall through to the default
// handler in go-telegram/bot, so storage has to happen here too).
func (b *Bot) pingHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	b.persistMessage(ctx, update.Message)

	if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: update.Message.Chat.ID,
		Text:   "pong",
	}); err != nil {
		b.log.Error("telegram: SendMessage /ping reply failed",
			slog.Int64("chat_id", update.Message.Chat.ID),
			slog.Any("err", err))
	}
}

// persistMessage upserts the chat row and stores the message body.
// Errors are logged but not propagated — telegram delivery is best-effort
// and the long-poll loop must keep running through transient DB hiccups.
func (b *Bot) persistMessage(ctx context.Context, m *models.Message) {
	chatJID := buildChatJID(m.Chat.ID)
	chatName := chatDisplayName(&m.Chat)
	tsMS := int64(m.Date) * 1000 // Telegram dates are unix seconds

	if err := b.store.UpsertChat(ctx, store.ChatRecord{
		JID:             chatJID,
		Name:            chatName,
		LastMessageTime: tsMS,
		IsGroup:         isGroupChat(m.Chat.Type),
	}); err != nil {
		b.log.Error("telegram: UpsertChat failed",
			slog.String("chat_jid", chatJID),
			slog.Any("err", err))
		// Continue: we still want to try to save the message itself.
	}

	msg := store.Message{
		ChatJID:      chatJID,
		TGMessageID:  int64(m.ID),
		Sender:       senderID(m.From),
		SenderName:   senderDisplayName(m.From),
		Content:      messageText(m),
		Timestamp:    tsMS,
		IsBotMessage: m.From != nil && m.From.IsBot,
	}
	if r := m.ReplyToMessage; r != nil {
		msg.ReplyToTGMessageID = int64(r.ID)
		msg.ReplyToContent = messageText(r)
		msg.ReplyToSenderName = senderDisplayName(r.From)
	}

	id, err := b.store.SaveMessage(ctx, msg)
	if err != nil {
		b.log.Error("telegram: SaveMessage failed",
			slog.String("chat_jid", chatJID),
			slog.Int64("tg_msg_id", msg.TGMessageID),
			slog.Any("err", err))
		return
	}
	b.log.Debug("telegram: stored message",
		slog.String("chat_jid", chatJID),
		slog.Int64("tg_msg_id", msg.TGMessageID),
		slog.Int64("row_id", id),
		slog.String("sender", msg.SenderName),
	)
}

// buildChatJID returns the picoclaw canonical "tg:<chat_id>" form. The
// telegram chat ID is signed (negative for groups) and we keep that as-is
// in the string for symmetry with NanoClaw.
func buildChatJID(chatID int64) string {
	return "tg:" + strconv.FormatInt(chatID, 10)
}

// isGroupChat reports whether a Telegram Chat.Type is one of the
// many-user kinds.
func isGroupChat(t models.ChatType) bool {
	switch t {
	case models.ChatTypeGroup, models.ChatTypeSupergroup, models.ChatTypeChannel:
		return true
	default:
		return false
	}
}

// chatDisplayName picks the human label for a chat: title for groups,
// constructed name for private chats.
func chatDisplayName(c *models.Chat) string {
	if c == nil {
		return ""
	}
	if c.Title != "" {
		return c.Title
	}
	parts := []string{c.FirstName, c.LastName}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name != "" {
		return name
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return ""
}

// senderID returns the Telegram numeric user ID as a string, or "" if
// the From field is missing (channel posts have no From).
func senderID(u *models.User) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

// senderDisplayName picks a friendly label for a Telegram user.
func senderDisplayName(u *models.User) string {
	if u == nil {
		return ""
	}
	parts := []string{u.FirstName, u.LastName}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return strconv.FormatInt(u.ID, 10)
}

// messageText returns the textual content of a message: Text for plain
// messages, Caption for media messages with a caption, otherwise an
// empty string. M1 only stores text — media handling lands in I2/I3/I4.
func messageText(m *models.Message) string {
	if m == nil {
		return ""
	}
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}
