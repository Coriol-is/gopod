package telegram

// Inline keyboards gopod attaches to its messages, and the callbacks of
// their buttons that act on a running turn.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf16"

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

// copyTextMax is the Bot API limit for CopyTextButton.text: "1-256
// characters" (https://core.telegram.org/bots/api#copytextbutton).
// Telegram counts characters in UTF-16 code units, so the check does
// too: an emoji outside the BMP counts twice.
const copyTextMax = 256

// replyKeyboard is the action row under a turn's newest reply (spec F3):
// 🔁 regenerate (rg:<turnID>), 📋 copy (copy_text, only when copyable)
// and ⋯ more (mo:<turnID>). The ids are decimal int64, far below the
// 64-byte callback data limit.
func replyKeyboard(turnID int64, reply string) models.InlineKeyboardMarkup {
	id := strconv.FormatInt(turnID, 10)
	rg, _ := encodeCB("rg", id)
	mo, _ := encodeCB("mo", id)
	row := []models.InlineKeyboardButton{{Text: btnRegenerate, CallbackData: rg}}
	if text, ok := copyable(reply); ok {
		row = append(row, models.InlineKeyboardButton{
			Text:     btnCopy,
			CopyText: &models.CopyTextButton{Text: text},
		})
	}
	row = append(row, models.InlineKeyboardButton{Text: btnMore, CallbackData: mo})
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{row}}
}

// moreKeyboard replaces the action row in place when ⋯ is pressed:
// steps (st2), as file (af) and back (mb), which restores replyKeyboard.
func moreKeyboard(turnID int64) models.InlineKeyboardMarkup {
	id := strconv.FormatInt(turnID, 10)
	st, _ := encodeCB("st2", id)
	af, _ := encodeCB("af", id)
	mb, _ := encodeCB("mb", id)
	return models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{{
		{Text: btnSteps, CallbackData: st},
		{Text: btnAsFile, CallbackData: af},
		{Text: btnBack, CallbackData: mb},
	}}}
}

// copyable returns what 📋 copies: the whole reply when it fits the
// copy_text limit, else the body of its only fenced code block when
// that fits. ok is false when neither does (no copy button).
func copyable(reply string) (string, bool) {
	if t := strings.TrimSpace(reply); fitsCopy(t) {
		return t, true
	}
	var blocks []string
	var body []string
	in := false
	for _, line := range strings.Split(reply, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if in {
				blocks = append(blocks, strings.Join(body, "\n"))
				body = nil
			}
			in = !in
			continue
		}
		if in {
			body = append(body, line)
		}
	}
	if len(blocks) == 1 {
		if code := strings.TrimSpace(blocks[0]); fitsCopy(code) {
			return code, true
		}
	}
	return "", false
}

// fitsCopy reports a non-empty text within copyTextMax UTF-16 units.
func fitsCopy(s string) bool {
	return s != "" && len(utf16.Encode([]rune(s))) <= copyTextMax
}
