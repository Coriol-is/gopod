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

// cmdButtonTTL is how long a command button works after it was shown;
// confirmTTL is the same for a confirmation's Yes and Cancel.
const (
	cmdButtonTTL = 10 * time.Minute
	confirmTTL   = 5 * time.Minute
)

// cmdButton is what a command button's token stands for.
type cmdButton struct {
	chatID    int64 // the chat the button was shown in
	name      string
	args      []string
	confirmed bool   // the Yes of a confirmation (cf)
	cancel    bool   // dismisses its message (cx)
	group     string // the buttons of one message; cf/cx spend it whole
	expires   time.Time
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
	group := newButtonToken()
	out := make([][]models.InlineKeyboardButton, 0, len(rows))
	for _, row := range rows {
		var kbRow []models.InlineKeyboardButton
		for _, btn := range row {
			tok := newButtonToken()
			e := cmdButton{
				chatID:    chatID,
				name:      btn.Command,
				args:      append([]string(nil), btn.Args...),
				confirmed: btn.Confirmed && btn.Command != "",
				cancel:    btn.Command == "",
				group:     group,
				expires:   now.Add(cmdButtonTTL),
			}
			act := "cc"
			switch {
			case e.confirmed:
				act, e.expires = "cf", now.Add(confirmTTL)
			case e.cancel:
				act, e.expires = "cx", now.Add(confirmTTL)
			}
			b.cmdBtns[tok] = e
			data, _ := encodeCB(act, tok) // 15 bytes
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

// pruneButtonsLocked drops expired tokens and pages. Called under btnMu.
func (b *Bot) pruneButtonsLocked(now time.Time) {
	for tok, e := range b.cmdBtns {
		if now.After(e.expires) {
			delete(b.cmdBtns, tok)
		}
	}
	for tok, p := range b.pages {
		if now.After(p.expires) {
			delete(b.pages, tok)
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

// spendConfirmation returns the live Yes (cancel false) or Cancel
// (cancel true) behind a pressed confirmation button and forgets every
// button of its message, so Yes and Cancel are spent together and Yes
// runs at most once, also when pressed twice at the same moment. A
// token pressed outside its chat is refused without spending it. toast
// is non-empty when nothing was taken.
func (b *Bot) spendConfirmation(q cbQuery, cancel bool) (e cmdButton, toast string) {
	if len(q.Data.Args) != 1 {
		return cmdButton{}, cbToastExpired
	}
	now := b.clock()
	b.btnMu.Lock()
	defer b.btnMu.Unlock()
	e, ok := b.cmdBtns[q.Data.Args[0]]
	if !ok || now.After(e.expires) || e.cancel != cancel || (!cancel && !e.confirmed) {
		return cmdButton{}, cbToastExpired
	}
	if !b.buttonInChat(e, q) {
		return cmdButton{}, cbToastNotYours
	}
	for t, other := range b.cmdBtns {
		if other.group == e.group {
			delete(b.cmdBtns, t)
		}
	}
	return e, ""
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
	if !ok || e.confirmed || e.cancel {
		return cbToastExpired, nil
	}
	if !b.buttonInChat(e, q) {
		return cbToastNotYours, nil
	}
	return b.runButtonCommand(ctx, q, control.Command{
		Name:   e.name,
		Args:   append([]string(nil), e.args...),
		Caller: b.callbackCaller(q),
	})
}

// confirmButton handles the Yes of a confirmation (cf:<token>): it
// spends the confirmation and dispatches its command with Confirmed
// through the Router, which checks Perm again.
func (b *Bot) confirmButton(ctx context.Context, q cbQuery) (string, error) {
	if b.router == nil {
		return cbToastExpired, nil
	}
	e, toast := b.spendConfirmation(q, false)
	if toast != "" {
		return toast, nil
	}
	return b.runButtonCommand(ctx, q, control.Command{
		Name:      e.name,
		Args:      append([]string(nil), e.args...),
		Caller:    b.callbackCaller(q),
		Confirmed: true,
	})
}

// cancelButton handles Cancel (cx:<token>): it spends the confirmation
// and edits the message to "cancelled" without buttons.
func (b *Bot) cancelButton(ctx context.Context, q cbQuery) (string, error) {
	if _, toast := b.spendConfirmation(q, true); toast != "" {
		return toast, nil
	}
	b.editCommandMessage(ctx, q.ChatID, q.MessageID, control.Response{Text: cmdTextCancelled})
	return "", nil
}

// buttonInChat reports whether a token is pressed in the chat it was
// shown in; a token replayed from another chat is refused and logged.
func (b *Bot) buttonInChat(e cmdButton, q cbQuery) bool {
	if e.chatID == q.ChatID {
		return true
	}
	b.log.Info("command button refused: shown in another chat",
		slog.Int64("chat_id", q.ChatID), slog.String("command", e.name))
	return false
}

// runButtonCommand dispatches cmd for a pressed button and shows the
// result in the button's message.
//
// The Router's refusal (permission, unknown command) is checked first
// and toasted; a picker stays as it was, a spent confirmation becomes
// the refusal text without buttons. Otherwise the query is answered
// before the command runs, so a slow command never leaves the client's
// spinner hanging, and the message is edited to the result, or to a
// failure text when the handler failed.
func (b *Bot) runButtonCommand(ctx context.Context, q cbQuery, cmd control.Command) (string, error) {
	if resp, err := b.router.Check(cmd); err != nil {
		if cmd.Confirmed {
			b.editCommandText(ctx, q.ChatID, q.MessageID, resp.Text, nil)
		}
		return resp.Text, nil
	}
	q.answerNow("")
	resp, err := b.router.Dispatch(ctx, cmd)
	if err != nil {
		text := resp.Text
		if !errors.Is(err, control.ErrNotAuthorized) && !errors.Is(err, control.ErrUnknownCommand) {
			text = cbToastFailed
		}
		b.editCommandText(ctx, q.ChatID, q.MessageID, text, nil)
		return cbToastFailed, fmt.Errorf("button command %s: %w", cmd.Name, err)
	}
	b.editCommandMessage(ctx, q.ChatID, q.MessageID, resp)
	return "", nil
}

const cmdTextTopicGone = "Тема удалена. Повторите команду в нужном диалоге — действие не выполнено."

// sendCommandResponse sends a command's Response as a reply to the
// command message: one message (paged when long, pages.go) with its
// buttons.
func (b *Bot) sendCommandResponse(ctx context.Context, chatID int64, thread, replyTo int, resp control.Response) {
	if resp.Text == "" && len(resp.Buttons) == 0 {
		return
	}
	text, markup := b.commandMessage(chatID, resp)
	if resp.Code == control.CodeConfirm {
		// Confirmation applies to the requested conversation. Moving its
		// buttons to main would change /clear's target when pressed.
		if !b.threadGone(chatID, thread) {
			_, err := b.sendChunks(ctx, chatID, thread, replyTo, text, markdownToTelegramHTML, markup)
			if !b.checkThreadGone(chatID, thread, err) {
				return
			}
		}
		_, _ = b.sendChunks(ctx, chatID, 0, 0, cmdTextTopicGone, markdownToTelegramHTML, nil)
		return
	}
	_, _ = b.sendRenderedOpts(ctx, chatID, thread, replyTo, text, markdownToTelegramHTML, markup, true)
}

// editCommandMessage replaces a command message (a picker, a
// confirmation) with resp in place: its text and its buttons, or none.
// Best-effort: a failed edit is logged only.
func (b *Bot) editCommandMessage(ctx context.Context, chatID int64, msgID int, resp control.Response) {
	text, markup := b.commandMessage(chatID, resp)
	b.editCommandText(ctx, chatID, msgID, text, markup)
}

// editCommandText edits a command message to text (markdown) and markup
// (nil: no buttons). Best-effort: a failure is logged only.
func (b *Bot) editCommandText(ctx context.Context, chatID int64, msgID int, text string, markup models.ReplyMarkup) {
	if markup == nil {
		markup = noKeyboard()
	}
	if err := b.editMessageHTMLKB(ctx, chatID, msgID, markdownToTelegramHTML(text), markup); err != nil {
		b.log.Debug("command message edit failed",
			slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
	}
}
