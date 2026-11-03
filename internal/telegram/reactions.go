package telegram

import (
	"context"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// react sets a single emoji reaction on a message. Best-effort:
// failures are logged at debug (reactions are UX sugar, not critical).
func (b *Bot) react(ctx context.Context, chatID int64, messageID int, emoji string) {
	err := b.topicEditRequest(ctx, chatID, messageID, func(ctx context.Context) error {
		_, e := b.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Reaction: []models.ReactionType{{
				Type: models.ReactionTypeTypeEmoji,
				ReactionTypeEmoji: &models.ReactionTypeEmoji{
					Emoji: emoji,
				},
			}},
		})
		return e
	})
	if err != nil {
		b.log.Debug("react failed",
			slog.Int64("chat_id", chatID),
			slog.String("emoji", emoji),
			slog.Any("err", err))
	}
}

// clearReaction removes all reactions from a message.
func (b *Bot) clearReaction(ctx context.Context, chatID int64, messageID int) {
	_ = b.topicEditRequest(ctx, chatID, messageID, func(ctx context.Context) error {
		_, e := b.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
			ChatID:    chatID,
			MessageID: messageID,
			Reaction:  []models.ReactionType{},
		})
		return e
	})
}

// Reaction emojis for direct react calls; a turn's states use the
// status reactor (status.go, stateEmoji). Must be from Telegram's
// allowed reaction list — not all Unicode emoji work. ✅ and ❌ are NOT
// valid reactions. See https://core.telegram.org/bots/api#reactiontypeemoji
const (
	emojiThinking = "👀"
	emojiDone     = "👍"
	emojiError    = "👎"
)
