package telegram

import (
	"context"
	"fmt"
	"strconv"

	bot "github.com/go-telegram/bot"

	"github.com/Coriol-is/gopod/internal/ipc"
)

// IPCSink adapts *Bot to the ipc.MessageSink interface.
type IPCSink struct {
	bot *Bot
}

// NewIPCSink wires the running bot into the IPC watcher.
func NewIPCSink(b *Bot) *IPCSink { return &IPCSink{bot: b} }

// Send delivers a plain text message to chatJID. JID is the int64
// chat id encoded as a base-10 string (matches store.RegisteredChat.JID).
func (a *IPCSink) Send(ctx context.Context, chatJID, text string) error {
	id, err := strconv.ParseInt(chatJID, 10, 64)
	if err != nil {
		return fmt.Errorf("parse chatJID %q: %w: %w", chatJID, err, ipc.ErrPermanent)
	}
	if a.bot == nil || a.bot.api == nil {
		return fmt.Errorf("telegram bot not initialised: %w", ipc.ErrPermanent)
	}
	if _, err := a.bot.api.SendMessage(ctx, &bot.SendMessageParams{
		ChatID: id,
		Text:   text,
	}); err != nil {
		return err
	}
	return nil
}
