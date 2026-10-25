package telegram

// Inline keyboards gopod attaches to its messages, and the callbacks of
// their buttons that act on a running turn.

import (
	"context"
	"log/slog"
	"strconv"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// stopKeyboard is the ⏹ button of a stoppable turn's placeholder (spec
// F2): data st:<turnID>. A decimal int64 id keeps the data far below 64
// bytes.
func stopKeyboard(turnID int64) models.InlineKeyboardMarkup {
	data, _ := encodeCB("st", strconv.FormatInt(turnID, 10))
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{{
		Text:         btnStop,
		Style:        "danger",
		CallbackData: data,
	}}}}
}

// noKeyboard is an explicit empty inline keyboard: an edit that carries
// it removes the message's buttons.
func noKeyboard() models.ReplyMarkup {
	return &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{}}
}

// turnKeyboard is the markup of a turn's placeholder while it streams:
// the Stop button when the turn is in flight (registerInflight), else
// none. Every text edit of the placeholder carries it, so text and
// buttons change in one call and never flicker.
func (b *Bot) turnKeyboard(turnID int64) models.ReplyMarkup {
	if b.inflightStop(turnID) == nil {
		return nil
	}
	kb := stopKeyboard(turnID)
	return &kb
}

// clearKeyboard removes a message's inline keyboard without touching its
// text. Best-effort: a failure is logged only.
func (b *Bot) clearKeyboard(ctx context.Context, chatID int64, msgID int) {
	_, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   msgID,
		ReplyMarkup: noKeyboard(),
	})
	if err != nil {
		b.log.Debug("clear keyboard failed",
			slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
	}
}

// stopCallback handles ⏹ (st:<turnID>): it stops the turn when it is
// still running in the chat the button sits in. A press after the turn
// ended, a second press, or the data replayed from another chat only
// toasts.
func (b *Bot) stopCallback(_ context.Context, q cbQuery) (string, error) {
	if len(q.Data.Args) != 1 {
		return cbToastExpired, nil
	}
	turnID, err := strconv.ParseInt(q.Data.Args[0], 10, 64)
	if err != nil || turnID <= 0 {
		return cbToastExpired, nil
	}
	if !b.stopTurn(turnID, q.ChatID) {
		return stopToastFinished, nil
	}
	return stopToastStopping, nil
}
