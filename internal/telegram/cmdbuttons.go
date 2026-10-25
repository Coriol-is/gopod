package telegram

// Command buttons (spec F7, D028): a control.Response may offer
// follow-up commands as control.Buttons. They are shown as inline
// buttons whose callback data is "cc:<token>"; the token maps, in
// memory and for cmdButtonTTL, to the command name, its args and the
// chat the button was shown in. Pressing one dispatches that command
// through the Router with a Caller built from the callback exactly as
// dispatchSlash builds one from a typed command, so Perm applies the
// same way, and the button's message is edited in place to the result.
//
// The data holds the token only: command names and args never reach
// Telegram (they could exceed 64 bytes, and D028 keeps callback data to
// ids). A restart forgets every token; a press then toasts "expired".

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
)

// cmdButtonTTL is how long a command button works after it was shown.
const cmdButtonTTL = 10 * time.Minute

// cmdButton is what a command button's token stands for.
type cmdButton struct {
	chatID  int64 // the chat the button was shown in
	name    string
	args    []string
	expires time.Time
}

// newButtonToken returns a random 12-character hex token.
func newButtonToken() string {
	var buf [6]byte
	_, _ = rand.Read(buf[:]) // crypto/rand.Read never fails (Go 1.24+)
	return hex.EncodeToString(buf[:])
}

// buttonStyle passes the Bot API styles through and drops anything else.
func buttonStyle(s string) string {
	switch s {
	case control.StylePrimary, control.StyleDanger, control.StyleSuccess:
		return s
	}
	return ""
}

// commandRows renders a Response's buttons for chatID, storing each
// button's command under a fresh token.
func (b *Bot) commandRows(chatID int64, rows [][]control.Button) [][]models.InlineKeyboardButton {
	if len(rows) == 0 {
		return nil
	}
	now := b.clock()
	b.btnMu.Lock()
	defer b.btnMu.Unlock()
	b.pruneButtonsLocked(now)
	if b.cmdBtns == nil {
		b.cmdBtns = make(map[string]cmdButton)
	}
	out := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		var kbRow []models.InlineKeyboardButton
		for _, btn := range row {
			tok := newButtonToken()
			b.cmdBtns[tok] = cmdButton{
				chatID:  chatID,
				name:    btn.Command,
				args:    append([]string(nil), btn.Args...),
				expires: now.Add(cmdButtonTTL),
			}
			data, _ := encodeCB("cc", tok) // 15 bytes
			kbRow = append(kbRow, models.InlineKeyboardButton{
				Text:         btn.Label,
				Style:        buttonStyle(btn.Style),
				CallbackData: data,
			})
		}
		if len(kbRow) > 0 {
			out = append(out, kbRow)
		}
	}
	return out
}

// pruneButtonsLocked drops expired tokens. Called under btnMu.
func (b *Bot) pruneButtonsLocked(now time.Time) {
	for tok, e := range b.cmdBtns {
		if now.After(e.expires) {
			delete(b.cmdBtns, tok)
		}
	}
}

// lookupButton returns the live command of a token.
func (b *Bot) lookupButton(tok string) (cmdButton, bool) {
	now := b.clock()
	b.btnMu.Lock()
	defer b.btnMu.Unlock()
	e, ok := b.cmdBtns[tok]
	if !ok || now.After(e.expires) {
		return cmdButton{}, false
	}
	return e, true
}

// callbackCaller is the control.Caller of a pressed button: the chat
// and thread of the message carrying it, the user who pressed, and the
// same IsOwner rule as a typed command (dispatchSlash).
func (b *Bot) callbackCaller(q cbQuery) control.Caller {
	return control.Caller{
		ChatID:   q.ChatID,
		ThreadID: q.ThreadID,
		UserID:   q.UserID,
		Source:   control.SourceTelegram,
		IsOwner:  b.ownerChatID != 0 && q.ChatID == b.ownerChatID,
	}
}

// commandButton handles a command button (cc:<token>): it dispatches
// the token's command through the Router and edits the button's message
// to the result. A refusal by the Router (permission, unknown command)
// is a toast and changes nothing.
func (b *Bot) commandButton(ctx context.Context, q cbQuery) (string, error) {
	if len(q.Data.Args) != 1 || b.router == nil {
		return cbToastExpired, nil
	}
	e, ok := b.lookupButton(q.Data.Args[0])
	if !ok {
		return cbToastExpired, nil
	}
	if e.chatID != q.ChatID {
		b.log.Info("command button refused: shown in another chat",
			slog.Int64("chat_id", q.ChatID), slog.String("command", e.name))
		return cbToastNotYours, nil
	}
	return b.runButtonCommand(ctx, q, control.Command{
		Name:   e.name,
		Args:   append([]string(nil), e.args...),
		Caller: b.callbackCaller(q),
	})
}

// runButtonCommand dispatches cmd for a pressed button and shows the
// result in the button's message.
func (b *Bot) runButtonCommand(ctx context.Context, q cbQuery, cmd control.Command) (string, error) {
	resp, err := b.router.Dispatch(ctx, cmd)
	if errors.Is(err, control.ErrNotAuthorized) || errors.Is(err, control.ErrUnknownCommand) {
		// The Router logged a denial; the message stays as it was.
		return resp.Text, nil
	}
	if err != nil {
		return "", fmt.Errorf("button command %s: %w", cmd.Name, err)
	}
	b.editCommandMessage(ctx, q.ChatID, q.MessageID, resp)
	return "", nil
}

// commandReplyMarkup renders a Response's buttons as a reply markup for
// chatID; nil when it has none.
func (b *Bot) commandReplyMarkup(chatID int64, resp control.Response) models.ReplyMarkup {
	rows := b.commandRows(chatID, resp.Buttons)
	if len(rows) == 0 {
		return nil
	}
	return &models.InlineKeyboardMarkup{InlineKeyboard: rows}
}

// sendCommandResponse sends a command's Response as a reply to the
// command message, with its buttons under the last chunk.
func (b *Bot) sendCommandResponse(ctx context.Context, chatID int64, thread, replyTo int, resp control.Response) {
	if resp.Text == "" && len(resp.Buttons) == 0 {
		return
	}
	text := resp.Text
	if text == "" {
		text = cmdTextDone
	}
	_, _ = b.sendRendered(ctx, chatID, thread, replyTo, text, markdownToTelegramHTML,
		b.commandReplyMarkup(chatID, resp))
}

// editCommandMessage replaces a command message (a picker, a
// confirmation) with resp in place: its text and its buttons, or none.
// Best-effort: a failed edit is logged only.
func (b *Bot) editCommandMessage(ctx context.Context, chatID int64, msgID int, resp control.Response) {
	text := resp.Text
	if text == "" {
		text = cmdTextDone
	}
	markup := b.commandReplyMarkup(chatID, resp)
	if markup == nil {
		markup = noKeyboard()
	}
	if err := b.editMessageHTMLKB(ctx, chatID, msgID, markdownToTelegramHTML(text), markup); err != nil {
		b.log.Debug("command message edit failed",
			slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
	}
}
