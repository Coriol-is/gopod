package telegram

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
)

type deleteOutcomeError struct{ cause error }

func (e *deleteOutcomeError) Error() string {
	return "message deletion outcome requires reconciliation"
}
func (e *deleteOutcomeError) Unwrap() error  { return e.cause }
func (e *deleteOutcomeError) NoReplay() bool { return true }

type deleteReceipt struct {
	attempt store.TelegramDeleteAttempt
	ack     bool
	chat    int64
	thread  int
}

// deletePlaceholder is the only direct deletion writer. Arbitration and durable
// admission cover the complete HTTP call; a held/unknown result forbids fallback.
func (b *Bot) deletePlaceholder(ctx context.Context, chat int64, msg int) error {
	release, err := b.lockMessageEdit(ctx, chat, msg)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	defer release()
	return b.topicEditRequest(ctx, chat, msg, func(ctx context.Context) error {
		return b.deletePlaceholderLocked(ctx, chat, msg)
	})
}
func (b *Bot) deletePlaceholderLocked(ctx context.Context, chat int64, msg int) error {
	p := &bot.DeleteMessageParams{ChatID: chat, MessageID: msg}
	if b.store == nil || chat <= 0 {
		_, err := b.api.DeleteMessage(ctx, p)
		return err
	}
	owner, covered, err := b.store.ResolveDirectEditOwner(ctx, chat, msg)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	if !covered {
		_, err = b.api.DeleteMessage(ctx, p)
		return err
	}
	raw, err := json.Marshal(p)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	sum := sha256.Sum256(raw)
	source := -1
	if origin, ok := ctx.Value(topicOutputOriginKey{}).(chatThread); ok {
		if origin.chat != chat {
			return &ownershipSendError{cause: store.ErrScopeMismatch}
		}
		source = origin.thread
	}
	a, err := b.store.BeginTelegramDelete(ctx, store.TelegramEditRequest{ChatID: chat, MessageID: msg, SourceThread: source, Method: "delete", RequestHash: hex.EncodeToString(sum[:])}, owner)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	if a.ID == "" {
		return &ownershipSendError{cause: store.ErrTelegramDeleteHeld}
	}
	settle := func(ack bool) error {
		return b.settleDeleteReceipt(ctx, deleteReceipt{a, ack, chat, owner.SourceThread})
	}
	if err = ctx.Err(); err != nil {
		if e := settle(false); e != nil {
			return &deleteOutcomeError{cause: e}
		}
		return &ownershipSendError{cause: err}
	}
	ack, err := b.api.DeleteMessage(ctx, p)
	// A boolean true is the exact DeleteMessage success receipt. Even nil-error
	// false is unknown; a not-found error does not prove our deletion happened.
	ack = ack && err == nil
	if !ack && (err == nil || !definiteTopicRefusal(err)) {
		return &deleteOutcomeError{cause: errors.Join(store.ErrTelegramDeleteHeld, err)}
	}
	if e := settle(ack); e != nil {
		return &deleteOutcomeError{cause: e}
	}
	if ack {
		return nil
	}
	return err
}
func (b *Bot) settleDeleteReceipt(ctx context.Context, r deleteReceipt) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := b.store.SettleTelegramDelete(settle, r.attempt, r.ack)
	if err != nil {
		b.receiptMu.Lock()
		defer b.receiptMu.Unlock()
		if b.deleteReceipts == nil {
			b.deleteReceipts = make(map[string]deleteReceipt)
		}
		if len(b.deleteReceipts) < messageReceiptLimit {
			b.deleteReceipts[r.attempt.ID] = r
		}
	}
	return err
}
func (b *Bot) reconcileDeleteReceipts(ctx context.Context, match func(deleteReceipt) bool) error {
	if b.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b.receiptMu.Lock()
	var pending []deleteReceipt
	for _, r := range b.deleteReceipts {
		if match == nil || match(r) {
			pending = append(pending, r)
		}
	}
	b.receiptMu.Unlock()
	var result error
	for _, r := range pending {
		err := b.store.SettleTelegramDelete(ctx, r.attempt, r.ack)
		if err == nil {
			b.receiptMu.Lock()
			if b.deleteReceipts[r.attempt.ID] == r {
				delete(b.deleteReceipts, r.attempt.ID)
			}
			b.receiptMu.Unlock()
		}
		result = errors.Join(result, err)
	}
	return result
}
