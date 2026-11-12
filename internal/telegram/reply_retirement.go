package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// cleanupReplyKeyboards is bounded best effort after new ACKs and on recovery.
// Callback authority is already revoked by ordering; failure here never retries
// inference or hides an ambiguous edit. Each surface gets at most one attempt.
func (b *Bot) cleanupReplyKeyboards(ctx context.Context) {
	if b.store == nil || b.api == nil || b.ownerChatID <= 0 {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	rows, err := b.store.ReplyRetirementCandidates(ctx, b.ownerChatID, b.replyCleanupAfter.Load())
	if err != nil {
		b.log.Debug("reply cleanup discovery held", "err", err)
		return
	}
	if len(rows) == 0 {
		b.replyCleanupAfter.Store(0)
	}
	for _, row := range rows {
		if ctx.Err() != nil {
			return
		}
		b.replyCleanupAfter.Store(row.Sequence)
		if err = b.retireReplyKeyboard(ctx, row); err != nil {
			b.log.Debug("reply keyboard cleanup held", "err", err)
		}
	}
}

func (b *Bot) retireReplyKeyboard(ctx context.Context, row store.ReplySurface) error {
	chat := b.ownerChatID
	if chat <= 0 || row.Conversation.Scope.JID != buildChatJID(chat) {
		return store.ErrScopeMismatch
	}
	message := int(row.MessageID)
	release, err := b.lockMessageEdit(ctx, chat, message)
	if err != nil {
		return err
	}
	defer release()
	return b.topicRequest(ctx, chat, row.Conversation.ThreadID, func(call context.Context) error {
		owner, covered, err := b.store.ResolveDirectEditOwner(call, chat, message)
		if err != nil {
			return err
		}
		if !covered || owner.Scope != row.Conversation.Scope || owner.SourceThread != row.Conversation.ThreadID {
			return store.ErrTelegramEditHeld
		}
		params := &bot.EditMessageReplyMarkupParams{ChatID: chat, MessageID: message, ReplyMarkup: &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}}
		attempt, err := b.store.BeginTelegramEdit(call, store.TelegramEditRequest{ChatID: chat, MessageID: message, SourceThread: row.Conversation.ThreadID, Method: "markup", RequestHash: store.ReplyRetirementHash(chat, message), RetireSequence: row.Sequence}, owner, false)
		if err != nil {
			return err
		}
		if err = call.Err(); err != nil {
			return errors.Join(err, b.settleEditReceipt(call, editReceipt{attempt, false, chat, row.Conversation.ThreadID}))
		}
		result, err := b.api.EditMessageReplyMarkup(call, params)
		ack := err == nil && result != nil && result.ID == message && result.Chat.ID == chat
		if errors.Is(err, bot.ErrorBadRequest) && isNotModified(err) {
			ack = true
		}
		if !ack && (err == nil || !definiteTopicRefusal(err)) {
			return &directEditError{cause: errors.Join(store.ErrTelegramEditHeld, err)}
		}
		return errors.Join(err, b.settleEditReceipt(call, editReceipt{attempt, ack, chat, row.Conversation.ThreadID}))
	})
}
