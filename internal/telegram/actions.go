package telegram

// Turn actions behind inline buttons: Retry and Login under an error
// reply; Regenerate, More/Back, Steps and As file under the newest
// reply (replyrow.go).

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// retriedTTL is how long a Retry press is remembered against a second
// tap on the same button.
const retriedTTL = time.Hour

// retryTurn handles 🔁 (rt:<turnID>) under an error reply: it enqueues
// the failed turn's original text again, as a new turn in the chat and
// topic the button sits in. The turn must belong to that chat. The new
// item's SourceID is "cb:"+callback query id, so turn dedup never
// swallows a retry and a redelivered update is one turn; a second tap
// (a new query id) is caught by the in-memory retried set, and the
// button is taken off the reply.
func (b *Bot) retryTurn(ctx context.Context, q cbQuery) (string, error) {
	if len(q.Data.Args) != 1 || b.queue == nil || b.store == nil {
		return cbToastExpired, nil
	}
	turnID, err := strconv.ParseInt(q.Data.Args[0], 10, 64)
	if err != nil || turnID <= 0 {
		return cbToastExpired, nil
	}
	turn, err := b.store.GetTurn(ctx, turnID)
	if errors.Is(err, store.ErrTurnNotFound) {
		return cbToastExpired, nil
	}
	if err != nil {
		return "", fmt.Errorf("retry turn %d: %w", turnID, err)
	}
	if turn.ChatID != q.ChatID {
		b.log.Info("retry refused: turn belongs to another chat",
			slog.Int64("turn", turnID), slog.Int64("chat_id", q.ChatID))
		return cbToastNotYours, nil
	}
	if !b.markRetried(turnID) {
		return retryToastAlready, nil
	}
	item := queue.Item{
		ChatID:    q.ChatID,
		MessageID: int(turn.TGMessageID),
		ThreadID:  q.ThreadID,
		Folder:    q.Chat.Folder,
		IsOwner:   q.Chat.IsOwner,
		Text:      turn.Text,
		FilePath:  turn.FilePath,
		IsVoice:   turn.IsVoice,
		Source:    "telegram",
		SourceID:  "cb:" + q.ID,
	}
	// WithoutCancel: like an inbound message, a retry is persisted even
	// when shutdown starts mid-callback.
	id, dup := b.queue.Enqueue(context.WithoutCancel(ctx), item)
	if dup {
		return retryToastAlready, nil
	}
	if id == 0 {
		// Not saved (the queue logged why): nothing will run. Keep the
		// button and let the user press it again.
		b.unmarkRetried(turnID)
		return retryToastFailed, nil
	}
	b.log.Info("turn retried by user",
		slog.Int64("turn", turnID), slog.Int64("chat_id", q.ChatID), slog.Int("thread", q.ThreadID))
	// Through the reactor, so a worker that already started the turn
	// (🤔) is not set back to queued.
	b.newStatusReactor(item.ChatID, item.MessageID).Set(stQueued)
	b.clearKeyboard(ctx, q.ChatID, q.MessageID)
	return retryToastQueued, nil
}

// markRetried records a Retry of turnID; false when it was already
// retried within retriedTTL.
func (b *Bot) markRetried(turnID int64) bool {
	now := b.clock()
	b.errMu.Lock()
	defer b.errMu.Unlock()
	if at, ok := b.retried[turnID]; ok && now.Sub(at) < retriedTTL {
		return false
	}
	if b.retried == nil {
		b.retried = make(map[int64]time.Time)
	}
	for id, at := range b.retried {
		if now.Sub(at) >= retriedTTL {
			delete(b.retried, id)
		}
	}
	b.retried[turnID] = now
	return true
}

// unmarkRetried forgets a Retry of turnID whose turn was not queued.
func (b *Bot) unmarkRetried(turnID int64) {
	b.errMu.Lock()
	defer b.errMu.Unlock()
	delete(b.retried, turnID)
}

// loginCallback handles 🔑 (lg) under an auth error reply: the /login
// flow for the chat the button sits in, answering in its topic. The
// flow waits for the agent's login URL, so it runs in the background
// and the press is answered at once.
func (b *Bot) loginCallback(ctx context.Context, q cbQuery) (string, error) {
	// The slot is claimed here, before answering, so a second press
	// (or /login) while this one starts gets a toast, not a second exec.
	if !b.logins.reserve(q.ChatID) {
		if b.logins.isStarting(q.ChatID) {
			return loginToastInStart, nil
		}
		return loginToastAlready, nil
	}
	b.clearKeyboard(ctx, q.ChatID, q.MessageID)
	// The flow needs the chat only; it is registered (the dispatcher
	// checked).
	m := &models.Message{ID: q.MessageID, Chat: models.Chat{ID: q.ChatID}}
	go b.loginReserved(ctx, m, q.ThreadID, q.Chat)
	return loginToastStarting, nil
}

// regeneratePrefix opens the prompt of a regenerated turn (owner Q2:
// same session). The CLIs cannot rewind a session, so the agent sees its
// first answer and is asked for another one.
const regeneratePrefix = "[gopod] The user asked for a different answer to the request below. " +
	"Answer it again in a different way; do not repeat your previous answer.\n\n"

// regeneratePrompt is the regenerated turn's text: the original request
// with one prefix, also when the turn was itself a regeneration.
func regeneratePrompt(text string) string {
	return regeneratePrefix + strings.TrimPrefix(text, regeneratePrefix)
}

// chatTurn resolves the turn a reply button names (args[0]) and checks
// it belongs to the chat the button sits in: the id in the callback
// data is never trusted on its own. toast is non-empty when the press
// must stop there.
func (b *Bot) chatTurn(ctx context.Context, q cbQuery) (turn store.Turn, toast string, err error) {
	if len(q.Data.Args) != 1 || b.store == nil {
		return store.Turn{}, cbToastExpired, nil
	}
	turnID, err := strconv.ParseInt(q.Data.Args[0], 10, 64)
	if err != nil || turnID <= 0 {
		return store.Turn{}, cbToastExpired, nil
	}
	turn, err = b.store.GetTurn(ctx, turnID)
	if errors.Is(err, store.ErrTurnNotFound) {
		return store.Turn{}, cbToastExpired, nil
	}
	if err != nil {
		return store.Turn{}, "", fmt.Errorf("load turn %d: %w", turnID, err)
	}
	if turn.ChatID != q.ChatID || turn.Source == "task" {
		b.log.Info("reply action refused: turn belongs to another chat",
			slog.String("act", q.Data.Act), slog.Int64("turn", turnID), slog.Int64("chat_id", q.ChatID))
		return store.Turn{}, cbToastNotYours, nil
	}
	return turn, "", nil
}

// regenerate handles 🔁 (rg:<turnID>) under a reply: the turn's original
// request runs again as a new turn in the same session, chat and topic
// the button sits in. Like Retry, the item's SourceID is "cb:"+query id
// (a redelivered update is one turn) and a second tap within the hour
// only toasts. The pressed row is removed; the new reply brings its own.
func (b *Bot) regenerate(ctx context.Context, q cbQuery) (string, error) {
	turn, toast, err := b.chatTurn(ctx, q)
	if toast != "" || err != nil {
		return toast, err
	}
	if b.queue == nil {
		return cbToastExpired, nil
	}
	if !b.markRegenerated(turn.ID) {
		return regenToastAlready, nil
	}
	item := queue.Item{
		ChatID:    q.ChatID,
		MessageID: int(turn.TGMessageID),
		ThreadID:  q.ThreadID,
		Folder:    q.Chat.Folder,
		IsOwner:   q.Chat.IsOwner,
		Text:      regeneratePrompt(turn.Text),
		FilePath:  turn.FilePath,
		IsVoice:   turn.IsVoice,
		Source:    "telegram",
		SourceID:  "cb:" + q.ID,
	}
	id, dup := b.queue.Enqueue(context.WithoutCancel(ctx), item)
	if dup {
		return regenToastAlready, nil
	}
	if id == 0 {
		b.unmarkRegenerated(turn.ID)
		return regenToastFailed, nil
	}
	b.log.Info("turn regenerated by user",
		slog.Int64("turn", turn.ID), slog.Int64("new_turn", id),
		slog.Int64("chat_id", q.ChatID), slog.Int("thread", q.ThreadID))
	// Through the reactor, like Retry: a worker that already started
	// the new turn (🤔) is not set back to queued.
	b.newStatusReactor(item.ChatID, item.MessageID).Set(stQueued)
	b.forgetReply(q.ChatID, q.ThreadID, q.MessageID)
	b.clearKeyboard(ctx, q.ChatID, q.MessageID)
	return regenToastQueued, nil
}

// markRegenerated records a Regenerate of turnID; false when it was
// already regenerated within retriedTTL.
func (b *Bot) markRegenerated(turnID int64) bool {
	now := b.clock()
	b.rowMu.Lock()
	defer b.rowMu.Unlock()
	if at, ok := b.rows.regenerated[turnID]; ok && now.Sub(at) < retriedTTL {
		return false
	}
	if b.rows.regenerated == nil {
		b.rows.regenerated = make(map[int64]time.Time)
	}
	for id, at := range b.rows.regenerated {
		if now.Sub(at) >= retriedTTL {
			delete(b.rows.regenerated, id)
		}
	}
	b.rows.regenerated[turnID] = now
	return true
}

// unmarkRegenerated forgets a Regenerate whose turn was not queued.
func (b *Bot) unmarkRegenerated(turnID int64) {
	b.rowMu.Lock()
	defer b.rowMu.Unlock()
	delete(b.rows.regenerated, turnID)
}

// moreRow handles ⋯ (mo:<turnID>): the action row is swapped in place
// for the More row. Nothing else changes.
func (b *Bot) moreRow(ctx context.Context, q cbQuery) (string, error) {
	turn, toast, err := b.chatTurn(ctx, q)
	if toast != "" || err != nil {
		return toast, err
	}
	kb := moreKeyboard(turn.ID)
	b.setKeyboard(ctx, q.ChatID, q.MessageID, &kb)
	return "", nil
}

// backRow handles ‹ (mb:<turnID>): the action row comes back. Copy needs
// the reply text, which only the memo has; without it (restart, expiry)
// the row comes back without Copy.
func (b *Bot) backRow(ctx context.Context, q cbQuery) (string, error) {
	turn, toast, err := b.chatTurn(ctx, q)
	if toast != "" || err != nil {
		return toast, err
	}
	memo, _ := b.turnMemoFor(turn.ID, q.ChatID)
	kb := replyKeyboard(turn.ID, memo.reply)
	b.setKeyboard(ctx, q.ChatID, q.MessageID, &kb)
	return "", nil
}

// showSteps handles 🧭 (st2:<turnID>): the turn's tool steps (D024
// lines, redacted like the draft) as an expandable quote replying to
// the reply, in its topic. Steps are kept in memory for memoTTL.
func (b *Bot) showSteps(ctx context.Context, q cbQuery) (string, error) {
	turn, toast, err := b.chatTurn(ctx, q)
	if toast != "" || err != nil {
		return toast, err
	}
	memo, ok := b.turnMemoFor(turn.ID, q.ChatID)
	if !ok || len(memo.steps) == 0 {
		return stepsToastNone, nil
	}
	text := stepsHeader + "\n" + strings.Join(memo.steps, "\n")
	quote := func(s string) string {
		return "<blockquote expandable>" + html.EscapeString(s) + "</blockquote>"
	}
	if _, err := b.sendRendered(ctx, q.ChatID, q.ThreadID, q.MessageID, text, quote, nil); err != nil {
		return "", fmt.Errorf("send steps of turn %d: %w", turn.ID, err)
	}
	return "", nil
}

// asFile handles 📄 (af:<turnID>): the full reply as reply.md, in the
// reply's topic (the D025 document helper, with its topic-gone
// fallback). The text is kept in memory for memoTTL.
func (b *Bot) asFile(ctx context.Context, q cbQuery) (string, error) {
	turn, toast, err := b.chatTurn(ctx, q)
	if toast != "" || err != nil {
		return toast, err
	}
	memo, ok := b.turnMemoFor(turn.ID, q.ChatID)
	if !ok || memo.reply == "" {
		return fileToastGone, nil
	}
	if _, err := b.sendDocument(ctx, q.ChatID, q.ThreadID, "reply.md", []byte(memo.reply), ""); err != nil {
		b.log.Warn("as-file send failed", slog.Int64("turn", turn.ID), slog.Any("err", err))
		return fileToastFailed, nil
	}
	return "", nil
}
