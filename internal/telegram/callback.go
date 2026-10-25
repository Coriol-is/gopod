package telegram

// Inline-button callbacks (spec F1). Every button gopod shows carries
// callback data "act:arg1:arg2" (ids only, at most 64 bytes, the Bot
// API limit). One dispatcher, callbackHandler, receives every
// callback_query and routes it by act to a handler registered with
// onCallback.
//
// Authorization never comes from the callback data: the chat is the
// chat of the message the button sits on (CallbackQuery.Message), which
// must be a registered chat, and the thread is that message's topic.
// Handlers that act on a turn check the turn belongs to that chat.
// Settings actions (later phases) dispatch a control.Command through
// the Router, so D011 permissions keep applying.
//
// Every query is answered exactly once (answerCallbackQuery), also on
// an unknown act, an unregistered chat, a handler error or a panic, so
// the client never shows a spinner that hangs.

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"unicode/utf8"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// cbMaxBytes is the Bot API limit for callback_data.
const cbMaxBytes = 64

// cbToastMax is the Bot API limit for an answerCallbackQuery text.
const cbToastMax = 200

// cbData is decoded callback data: the action and its arguments.
type cbData struct {
	Act  string
	Args []string
}

// encodeCB builds callback data "act:arg1:...". It fails when the act is
// empty, a part contains ':', or the result exceeds 64 bytes.
func encodeCB(act string, args ...string) (string, error) {
	if act == "" || strings.Contains(act, ":") {
		return "", fmt.Errorf("callback act %q: empty or contains ':'", act)
	}
	for _, a := range args {
		if strings.Contains(a, ":") {
			return "", fmt.Errorf("callback arg %q contains ':'", a)
		}
	}
	s := strings.Join(append([]string{act}, args...), ":")
	if len(s) > cbMaxBytes {
		return "", fmt.Errorf("callback data %d bytes, limit %d", len(s), cbMaxBytes)
	}
	return s, nil
}

// decodeCB parses callback data. ok is false for empty or oversized data
// or an empty act.
func decodeCB(s string) (cbData, bool) {
	if s == "" || len(s) > cbMaxBytes {
		return cbData{}, false
	}
	parts := strings.Split(s, ":")
	if parts[0] == "" {
		return cbData{}, false
	}
	return cbData{Act: parts[0], Args: parts[1:]}, true
}

// cbQuery is one pressed button, resolved: the chat and thread come from
// the message carrying the button, never from Data.
type cbQuery struct {
	ID        string // callback query id
	ChatID    int64
	ThreadID  int // private-chat topic of the button's message (inboundThreadID)
	MessageID int // message carrying the button
	UserID    int64
	Chat      store.RegisteredChat
	Data      cbData
}

// cbHandler acts on a pressed button. toast is shown to the user; on an
// error the dispatcher logs it and shows a generic toast instead (the
// error text never reaches the chat).
type cbHandler func(ctx context.Context, q cbQuery) (toast string, err error)

// onCallback registers h for callback data whose act is act. It panics
// when act is already registered.
func (b *Bot) onCallback(act string, h cbHandler) {
	b.cbMu.Lock()
	defer b.cbMu.Unlock()
	if b.callbacks == nil {
		b.callbacks = make(map[string]cbHandler)
	}
	if _, dup := b.callbacks[act]; dup {
		// Two features claiming one act is a programming error: fail
		// at startup, not by silently routing one feature's buttons
		// to the other.
		panic("telegram: callback act " + act + " registered twice")
	}
	b.callbacks[act] = h
}

// registerCallbacks registers gopod's button actions. One line per act.
func (b *Bot) registerCallbacks() {
	b.onCallback("st", b.stopCallback)  // ⏹ on an edit-path placeholder
	b.onCallback("rt", b.retryTurn)     // 🔁 under an error reply
	b.onCallback("lg", b.loginCallback) // 🔑 under an auth error reply
	b.onCallback("rg", b.regenerate)    // 🔁 under the newest reply
	b.onCallback("mo", b.moreRow)       // ⋯ under the newest reply
	b.onCallback("mb", b.backRow)       // ‹ in the More row
	b.onCallback("st2", b.showSteps)    // 🧭 in the More row
	b.onCallback("af", b.asFile)        // 📄 in the More row
}

// callbackHandler is the dispatcher for every callback_query.
func (b *Bot) callbackHandler(ctx context.Context, _ *bot.Bot, upd *models.Update) {
	if upd == nil || upd.CallbackQuery == nil {
		return
	}
	cq := upd.CallbackQuery
	toast := ""
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("callback handler panicked",
				slog.String("data", cq.Data), slog.Any("panic", r))
			toast = cbToastFailed
		}
		b.answerCallback(ctx, cq.ID, toast)
	}()
	toast = b.dispatchCallback(ctx, cq)
}

// dispatchCallback resolves and runs one callback, returning its toast.
func (b *Bot) dispatchCallback(ctx context.Context, cq *models.CallbackQuery) string {
	data, ok := decodeCB(cq.Data)
	if !ok {
		return cbToastExpired
	}
	b.cbMu.RLock()
	h := b.callbacks[data.Act]
	b.cbMu.RUnlock()
	if h == nil {
		b.log.DebugContext(ctx, "callback for unknown act", slog.String("act", data.Act))
		return cbToastExpired
	}
	// A message older than 48 h arrives inaccessible; an inline-mode
	// button has none. Neither names a chat gopod can act in.
	m := cq.Message.Message
	if cq.Message.Type != models.MaybeInaccessibleMessageTypeMessage || m == nil {
		return cbToastExpired
	}
	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		b.log.InfoContext(ctx, "callback from unregistered chat ignored",
			slog.Int64("chat_id", m.Chat.ID), slog.String("act", data.Act))
		return cbToastNotRegistered
	}
	q := cbQuery{
		ID:        cq.ID,
		ChatID:    m.Chat.ID,
		ThreadID:  inboundThreadID(m),
		MessageID: m.ID,
		UserID:    cq.From.ID,
		Chat:      rc,
		Data:      data,
	}
	toast, err := h(ctx, q)
	if err != nil {
		b.log.ErrorContext(ctx, "callback action failed",
			slog.String("act", data.Act),
			slog.Int64("chat_id", q.ChatID),
			slog.Any("err", err))
		return cbToastFailed
	}
	return toast
}

// answerCallback answers a query, with an optional toast (redacted and
// cut to the Bot API limit). Failures are logged only.
func (b *Bot) answerCallback(ctx context.Context, id, toast string) {
	toast = redactSecrets(toast)
	if utf8.RuneCountInString(toast) > cbToastMax {
		toast = truncateRunes(toast, cbToastMax)
	}
	_, err := b.api.AnswerCallbackQuery(context.WithoutCancel(ctx), &bot.AnswerCallbackQueryParams{
		CallbackQueryID: id,
		Text:            toast,
	})
	if err != nil {
		b.log.Debug("answerCallbackQuery failed", slog.Any("err", err))
	}
}
