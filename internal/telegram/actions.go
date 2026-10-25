package telegram

// Turn actions behind inline buttons. Phase 1 has Retry (on an error
// reply) and Login; Regenerate, Steps and As-file come later.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
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
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiThinking)
	}
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
		return loginToastAlready, nil
	}
	b.clearKeyboard(ctx, q.ChatID, q.MessageID)
	// The flow needs the chat only; it is registered (the dispatcher
	// checked).
	m := &models.Message{ID: q.MessageID, Chat: models.Chat{ID: q.ChatID}}
	go b.loginReserved(ctx, m, q.ThreadID, q.Chat)
	return loginToastStarting, nil
}
