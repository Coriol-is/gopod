package telegram

// Inline keyboards gopod attaches to its messages, and the callbacks of
// their buttons that act on a running turn.

import (
	"context"
	"log/slog"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/Coriol-is/gopod/internal/store"
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

// hasButtons reports a markup that shows at least one inline button.
func hasButtons(m models.ReplyMarkup) bool {
	kb, ok := m.(*models.InlineKeyboardMarkup)
	if !ok || kb == nil {
		return false
	}
	for _, row := range kb.InlineKeyboard {
		if len(row) > 0 {
			return true
		}
	}
	return false
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
	b.inflightMu.Lock()
	if t := b.inflight[turnID]; t != nil {
		kb.InlineKeyboard[0][0].CallbackData, _ = encodeCB("st", strconv.FormatInt(turnID, 10), t.token)
	}
	b.inflightMu.Unlock()
	return &kb
}

// clearKeyboard removes a message's inline keyboard without touching its
// text. Best-effort: a failure is logged only.
func (b *Bot) clearKeyboard(ctx context.Context, chatID int64, msgID int) {
	b.setKeyboard(ctx, chatID, msgID, noKeyboard())
}

// setKeyboard replaces a message's inline keyboard without touching its
// text. Best-effort: a failure is logged only.
func (b *Bot) setKeyboard(ctx context.Context, chatID int64, msgID int, markup models.ReplyMarkup) {
	_, err := b.api.EditMessageReplyMarkup(ctx, &bot.EditMessageReplyMarkupParams{
		ChatID:      chatID,
		MessageID:   msgID,
		ReplyMarkup: markup,
	})
	if err != nil {
		b.log.Debug("set keyboard failed",
			slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
	}
}

// stopCallback handles ⏹ (st:<turnID>): it stops the turn when it is
// still running in the chat the button sits in. A press after the turn
// ended, a second press, or the data replayed from another chat only
// toasts.
func (b *Bot) stopCallback(ctx context.Context, q cbQuery) (string, error) {
	if len(q.Data.Args) < 1 || len(q.Data.Args) > 2 {
		return cbToastExpired, nil
	}
	id, err := strconv.ParseInt(q.Data.Args[0], 10, 64)
	if err != nil || id <= 0 {
		return cbToastExpired, nil
	}
	b.inflightMu.Lock()
	t := b.inflight[id]
	valid := t != nil && t.home == q.ChatID && t.thread == q.ThreadID && len(q.Data.Args) == 2 && q.Data.Args[1] == t.token && t.messages[q.MessageID]
	var scope store.ScopeRef
	if t != nil {
		scope = t.scope
	}
	b.inflightMu.Unlock()
	if valid && b.store != nil {
		valid = b.store.ValidateScope(ctx, scope) == nil
	}
	if valid && t.stop() {
		return stopToastStopping, nil
	}
	status := b.controlTurnStatus(ctx, id, q.ChatID, q.ThreadID, q.MessageID)
	q.answerNow(status)
	if b.store != nil {
		if binding, e := b.store.GetControlMessage(ctx, q.ChatID, q.MessageID); e == nil && binding.TurnID == id && binding.ThreadID == q.ThreadID && b.store.ValidateScope(ctx, binding.Scope) == nil {
			_ = b.store.RequeueControlRepair(ctx, q.ChatID, q.MessageID, binding.Token)
		}
	}
	return status, nil
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

// copyable returns what 📋 copies: the whole reply as the user sees it
// (markup removed) when it fits the copy_text limit, else the body of
// its only fenced code block when that fits. ok is false when neither
// does (no copy button).
func copyable(reply string) (string, bool) {
	// The rendered text, not the markdown: **bold** copies as bold.
	if t := strings.TrimSpace(htmlToPlain(markdownToTelegramHTML(reply))); fitsCopy(t) {
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
