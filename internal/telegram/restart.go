package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// SendRestartNotice sends exactly once to the initiating conversation. No
// fallback to main, formatting retry or automatic resend after an unknown send.
func (b *Bot) SendRestartNotice(ctx context.Context, chatID int64, threadID int, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var msg *models.Message
	err := b.topicRequest(ctx, chatID, threadID, func(ctx context.Context) error {
		var e error
		msg, e = b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: threadID, Text: text})
		return e
	})
	if err != nil {
		return fmt.Errorf("restart notification not acknowledged: %w", err)
	}
	if msg == nil || msg.ID <= 0 {
		return fmt.Errorf("restart notification acknowledgement missing")
	}
	return nil
}
