package telegram

import (
	"context"
	"errors"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// topicAdmission checks durable intent/tombstone state before new topic work.
// Main, groups and unregistered diagnostics keep their existing behavior. This
// is a read fence, not a lease: an already issued request still needs quiescence.
func (b *Bot) topicAdmission(ctx context.Context, chatID int64, thread int) error {
	if thread <= 0 || chatID <= 0 || b.store == nil {
		return nil
	}
	_, err := b.store.GetRegistered(ctx, buildChatJID(chatID))
	if errors.Is(err, store.ErrChatNotRegistered) {
		return nil
	}
	if err != nil {
		return err
	}
	scope, err := b.store.SnapshotConversation(ctx, buildChatJID(chatID), thread, store.VisibilityConversation)
	if err != nil {
		return err
	}
	return b.store.ValidateConversation(ctx, scope)
}

// Keep this inside offsetMiddleware: rejected input must still advance the
// polling cursor, but must not reach login, media download, persistence or tools.
func (b *Bot) topicAdmissionMiddleware(next bot.HandlerFunc) bot.HandlerFunc {
	return func(ctx context.Context, api *bot.Bot, update *models.Update) {
		if update != nil && update.Message != nil {
			m := update.Message
			if m.Chat.Type == models.ChatTypePrivate && b.topicAdmission(ctx, m.Chat.ID, inboundThreadID(m)) != nil {
				return
			}
		}
		next(ctx, api, update)
	}
}
