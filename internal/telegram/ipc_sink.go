package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

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
// chat id in canonical tg:<id> form (matches store.RegisteredChat.JID).
// Bare numeric ids remain accepted for legacy owner IPC clients.
func (a *IPCSink) Send(ctx context.Context, chatJID, text string) error {
	id, err := strconv.ParseInt(strings.TrimPrefix(chatJID, "tg:"), 10, 64)
	if err != nil {
		return fmt.Errorf("parse chatJID %q: %w: %w", chatJID, err, ipc.ErrPermanent)
	}
	if a.bot == nil || a.bot.api == nil {
		return fmt.Errorf("telegram bot not initialised: %w", ipc.ErrPermanent)
	}
	// No MessageThreadID: IPC messages are agent-authored and carry no
	// conversation, so they always land in the main chat (thread 0),
	// never in a private-chat topic. Deliberate; see the threads spec.
	// Bound by bytes, hence also below Telegram's character limit. Preserve
	// UTF-8 and every byte, including newlines, in long scheduled digests.
	parts := ipcMessageParts(text)
	for i, part := range parts {
		if _, err := a.bot.api.SendMessage(ctx, &bot.SendMessageParams{ChatID: id, Text: part}); err != nil {
			if i > 0 {
				return fmt.Errorf("partial delivery: %d of %d parts accepted; remaining delivery uncertain, do not retry whole message: %w: %w", i, len(parts), err, ipc.ErrPermanent)
			}
			if errors.Is(err, bot.ErrorBadRequest) || errors.Is(err, bot.ErrorForbidden) || errors.Is(err, bot.ErrorUnauthorized) || errors.Is(err, bot.ErrorNotFound) {
				return fmt.Errorf("Telegram rejected message: %w: %w", err, ipc.ErrPermanent)
			}
			return err
		}
	}
	return nil
}

func ipcMessageParts(text string) []string {
	const limit = 3500
	var parts []string
	for len(text) > limit {
		cut := limit
		for !utf8.RuneStart(text[cut]) {
			cut--
		}
		parts = append(parts, text[:cut])
		text = text[cut:]
	}
	return append(parts, text)
}
