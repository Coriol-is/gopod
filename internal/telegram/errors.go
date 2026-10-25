package telegram

// Humane error replies (spec F4). A failed agent run gets a short reply
// saying what happened and what to do, with one button: 🔑 login for an
// expired or missing login, 🔁 retry otherwise. The error itself goes
// to the log only; no stderr, path or token reaches the chat. The same
// kind of error in the same chat is answered at most once per
// errCooldown; repeats only set the 👎 reaction.

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// errKind is what the user is told about a failed turn.
type errKind int

const (
	errAuthExpired errKind = iota // the agent's login session expired
	errNotLoggedIn                // the agent container was never logged in
	errAgentFailed                // the run crashed, timed out or failed otherwise
	errBusy                       // no agent slot; reserved: the D026 gate waits without a timeout today
	errUsageLimit                 // provider allowance exhausted
	errDelivery                   // the reply could not be delivered
)

func (k errKind) String() string {
	switch k {
	case errAuthExpired:
		return "auth_expired"
	case errNotLoggedIn:
		return "not_logged_in"
	case errBusy:
		return "busy"
	case errUsageLimit:
		return "usage_limit"
	case errDelivery:
		return "delivery"
	}
	return "agent_failed"
}

// errCooldown is how long a repeat of the same error kind in the same
// chat gets no reply of its own. Quota notices use the failed turn id so
// another topic or the fallback attempt still receives its own notification.
const errCooldown = 60 * time.Second

// errCooldownKey names one (chat, kind) cooldown.
type errCooldownKey struct {
	chat      int64
	kind      errKind
	quotaTurn int64 // quota notices are per failed turn, including the fallback attempt
}

// errReply is one row of the error table: the text and the button.
type errReply struct {
	text  string
	login bool // 🔑 login button
	retry bool // 🔁 retry button, when the turn can be retried
}

var errReplies = map[errKind]errReply{
	errAuthExpired: {text: errTextAuthExpired, login: true},
	errNotLoggedIn: {text: errTextNotLoggedIn, login: true},
	errUsageLimit:  {text: errTextUsageLimit, retry: true},
	errAgentFailed: {text: errTextAgentFailed, retry: true},
	errBusy:        {text: errTextBusy, retry: true},
	errDelivery:    {text: errTextDelivery, retry: true},
}

// classifyErr maps a failed run's error to what the user is told.
func classifyErr(err error) errKind {
	switch {
	case errors.Is(err, runner.ErrUsageLimit):
		return errUsageLimit
	case errors.Is(err, runner.ErrSessionExpired):
		return errAuthExpired
	case errors.Is(err, runner.ErrNotLoggedIn):
		return errNotLoggedIn
	case errors.Is(err, errReplyUndelivered):
		return errDelivery
	}
	return errAgentFailed
}

// replyError tells the user a turn failed: 😱 on their message, and,
// unless the same kind of error was answered in this chat within
// errCooldown, a reply from the error table in the turn's topic.
func (b *Bot) replyError(ctx context.Context, item queue.Item, err error) {
	kind := classifyErr(err)
	b.turnStatus(item).Set(stError)
	if kind == errAuthExpired || kind == errNotLoggedIn {
		b.log.Error("agent authentication unavailable", slog.String("chat_folder", item.Folder), slog.Int64("turn", item.ID), slog.String("kind", kind.String()))
	} else {
		b.log.Error("agent run failed", slog.String("chat_folder", item.Folder), slog.Int64("turn", item.ID), slog.String("kind", kind.String()), slog.Any("err", err))
	}
	quotaTurn := int64(0)
	if kind == errUsageLimit {
		quotaTurn = item.ID
	}
	if !b.errCooldownPass(item.ChatID, kind, quotaTurn) {
		b.log.Debug("error reply suppressed by cooldown",
			slog.Int64("chat_id", item.ChatID), slog.String("kind", kind.String()))
		return
	}
	if kind == errUsageLimit && b.offerProviderFallback(ctx, item, err) {
		return
	}
	row := errReplies[kind]
	var buttons []models.InlineKeyboardButton
	if row.login && item.ChatID > 0 {
		data, _ := encodeCB("lg")
		buttons = append(buttons, models.InlineKeyboardButton{Text: btnLogin, CallbackData: data})
	}
	if row.retry && b.canRetry(item) {
		data, _ := encodeCB("rt", strconv.FormatInt(item.ID, 10))
		buttons = append(buttons, models.InlineKeyboardButton{Text: btnRetry, CallbackData: data})
	}
	var markup models.ReplyMarkup
	if len(buttons) > 0 {
		markup = &models.InlineKeyboardMarkup{InlineKeyboard: [][]models.InlineKeyboardButton{buttons}}
	}
	b.sendErrorReply(ctx, item, row.text, markup)
}

// canRetry reports whether a Retry button can re-run item: a durable
// turn of a Telegram chat, with a queue to put the retry on.
func (b *Bot) canRetry(item queue.Item) bool {
	return item.ID != 0 && item.ChatID != 0 && b.queue != nil && b.store != nil
}

// errCooldownPass reports whether an error reply of kind may go out in
// chatID now, and if so starts that pair's cooldown.
func (b *Bot) errCooldownPass(chatID int64, kind errKind, quotaTurn int64) bool {
	now := b.clock()
	k := errCooldownKey{chat: chatID, kind: kind, quotaTurn: quotaTurn}
	b.errMu.Lock()
	defer b.errMu.Unlock()
	if last, ok := b.errShown[k]; ok && now.Sub(last) < errCooldown {
		return false
	}
	if b.errShown == nil {
		b.errShown = make(map[errCooldownKey]time.Time)
	}
	for key, at := range b.errShown {
		if now.Sub(at) >= errCooldown {
			delete(b.errShown, key)
		}
	}
	b.errShown[k] = now
	return true
}

func (b *Bot) clock() time.Time {
	if b.now != nil {
		return b.now()
	}
	return time.Now()
}

// sendErrorReply sends text with markup as a reply to the turn's message
// in its topic, or to the main chat when the topic is gone. Plain text:
// the table holds no markup. Failures are logged only.
func (b *Bot) sendErrorReply(ctx context.Context, item queue.Item, text string, markup models.ReplyMarkup) {
	send := func(thread, replyTo int, text string) error {
		params := &bot.SendMessageParams{
			ChatID:          item.ChatID,
			MessageThreadID: thread,
			Text:            text,
			ReplyMarkup:     markup,
		}
		if replyTo > 0 {
			params.ReplyParameters = &models.ReplyParameters{
				MessageID:                replyTo,
				AllowSendingWithoutReply: true,
			}
		}
		_, err := b.api.SendMessage(ctx, params)
		return err
	}
	var err error
	if !b.threadGone(item.ChatID, item.ThreadID) {
		err = send(item.ThreadID, item.MessageID, text)
		if !b.checkThreadGone(item.ChatID, item.ThreadID, err) {
			if err != nil {
				b.log.Warn("error reply failed",
					slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
			}
			return
		}
	}
	if err = send(0, 0, threadGoneNote+text); err != nil {
		b.log.Warn("error reply failed",
			slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
	}
}
