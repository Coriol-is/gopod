package telegram

import (
	"context"
	"errors"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const messageReceiptLimit = 128
const messageReceiptRetry = 30 * time.Second

type messageReceipt struct {
	owner     store.TelegramMessageOwner
	message   int
	retryAt   time.Time
	repairing bool
}

// retainMessageReceipt is called only after SendMessage returned a positive ID.
// Capacity never causes eviction or remote replay: an unretained receipt leaves
// its durable pending attempt and generic uncertainty intact.
func (b *Bot) retainMessageReceipt(owner store.TelegramMessageOwner, message int) bool {
	if owner.AttemptID == "" || message <= 0 {
		return false
	}
	b.receiptMu.Lock()
	defer b.receiptMu.Unlock()
	if b.messageReceipts == nil {
		b.messageReceipts = make(map[string]messageReceipt)
	}
	if old, ok := b.messageReceipts[owner.AttemptID]; ok {
		return old.owner == owner && old.message == message
	}
	if len(b.messageReceipts) >= messageReceiptLimit {
		return false
	}
	b.messageReceipts[owner.AttemptID] = messageReceipt{owner: owner, message: message}
	return true
}

// ReconcileMessageReceipts retries DB acknowledgement only. The bounded registry
// exists for this process, not as a reconstruction of pre-crash remote outcomes.
func (b *Bot) ReconcileMessageReceipts(ctx context.Context) error {
	return b.reconcileMessageReceipts(ctx, func(messageReceipt) bool { return true })
}
func (b *Bot) reconcileMessageReceipts(ctx context.Context, match func(messageReceipt) bool) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.store == nil {
		return nil
	}
	b.receiptMu.Lock()
	pending := make([]messageReceipt, 0, len(b.messageReceipts))
	var result error
	now := time.Now()
	for id, r := range b.messageReceipts {
		if !match(r) {
			continue
		}
		if r.repairing || now.Before(r.retryAt) {
			result = errors.Join(result, store.ErrMessageOwnership)
			continue
		}
		r.repairing = true
		b.messageReceipts[id] = r
		pending = append(pending, r)
	}
	b.receiptMu.Unlock()
	for _, r := range pending {
		err := ctx.Err()
		if err == nil {
			err = b.store.ReconcileTelegramMessageOwner(ctx, r.owner, r.message)
		}
		b.receiptMu.Lock()
		current, ok := b.messageReceipts[r.owner.AttemptID]
		if ok && current.owner == r.owner && current.message == r.message {
			if err == nil {
				delete(b.messageReceipts, r.owner.AttemptID)
			} else {
				current.repairing = false
				current.retryAt = time.Now().Add(messageReceiptRetry)
				b.messageReceipts[r.owner.AttemptID] = current
			}
		}
		b.receiptMu.Unlock()
		result = errors.Join(result, err)
	}
	return result
}
