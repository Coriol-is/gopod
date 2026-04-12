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
	_, err := b.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
		ChatID:    chatID,
		MessageID: messageID,
		Reaction: []models.ReactionType{{
			Type: models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{
				Emoji: emoji,
			},
		}},
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
	b.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
		ChatID:    chatID,
		MessageID: messageID,
		Reaction:  []models.ReactionType{},
	})
}

// Standard reaction emojis. Must be from Telegram's allowed reaction
// list — not all Unicode emoji work. ✅ and ❌ are NOT valid reactions.
// See https://core.telegram.org/bots/api#reactiontypeemoji
const (
	emojiThinking = "👀"
	emojiDone     = "👍"
	emojiError    = "👎"
)
