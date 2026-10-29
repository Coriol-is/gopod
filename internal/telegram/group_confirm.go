package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
)

const (
	groupConfirmUnreachable = "Could not reach the owner's private chat. Open a chat with the bot and send the command again."
	groupConfirmUnavailable = "This action needs a private confirmation, which is unavailable."
)

// sendOwnerConfirmation delivers a group command's confirmation to the owner's
// private chat (D048). The buttons are bound to that chat, so pressing them runs
// the command as the private owner; nothing in the group can confirm it. The
// group only gets a one-line acknowledgement.
func (b *Bot) sendOwnerConfirmation(ctx context.Context, m *models.Message, thread int, resp control.Response, status *statusReactor) {
	if resp.Code != control.CodeConfirm || b.ownerChatID <= 0 {
		status.Set(stError)
		b.replyTo(ctx, m.Chat.ID, thread, m.ID, groupConfirmUnavailable)
		return
	}
	text, markup := b.commandMessage(b.ownerChatID, resp)
	if _, err := b.sendChunks(ctx, b.ownerChatID, 0, 0, text, markdownToTelegramHTML, markup); err != nil {
		b.log.Warn("private confirmation not delivered", slog.Int64("chat_id", m.Chat.ID), slog.Any("err", err))
		status.Set(stError)
		b.replyTo(ctx, m.Chat.ID, thread, m.ID, groupConfirmUnreachable)
		return
	}
	status.Set(stDone)
	b.replyTo(ctx, m.Chat.ID, thread, m.ID, resp.Ack)
}
