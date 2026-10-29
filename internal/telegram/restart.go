package telegram

import (
	"context"
	"fmt"
	"time"

	"github.com/go-telegram/bot"
)

// SendRestartNotice sends exactly once to the initiating conversation. No
// fallback to main, formatting retry or automatic resend after an unknown send.
func (b *Bot) SendRestartNotice(ctx context.Context, chatID int64, threadID int, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	msg, err := b.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: chatID, MessageThreadID: threadID, Text: text})
	if err != nil {
		return fmt.Errorf("restart notification not acknowledged: %w", err)
	}
	if msg == nil || msg.ID <= 0 {
		return fmt.Errorf("restart notification acknowledgement missing")
	}
	return nil
}
