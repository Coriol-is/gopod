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
	"github.com/go-telegram/bot/models"
)

// This error is backed by a pending durable attempt, not generic process-local
// topic uncertainty. It forbids fallback/retry of the remote request.
type repairOutcomeError struct{ cause error }

func (e *repairOutcomeError) Error() string  { return "control repair outcome requires reconciliation" }
func (e *repairOutcomeError) Unwrap() error  { return e.cause }
func (e *repairOutcomeError) NoReplay() bool { return true }

type repairReceipt struct {
	attempt store.ControlRepairAttempt
	ack     bool
}

func (b *Bot) repairEdit(ctx context.Context, c store.ControlMessage, text string, parse models.ParseMode, final bool) error {
	p := &bot.EditMessageTextParams{ChatID: c.ChatID, MessageID: c.MessageID, Text: text, ParseMode: parse, ReplyMarkup: noKeyboard()}
	raw, err := json.Marshal(p)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	sum := sha256.Sum256(raw)
	return b.topicEditRequest(ctx, c.ChatID, c.MessageID, func(callCtx context.Context) error {
		a, err := b.store.BeginControlRepair(callCtx, c, hex.EncodeToString(sum[:]), final)
		if err != nil {
			return &ownershipSendError{cause: err}
		}
		if err = callCtx.Err(); err != nil {
			if e := b.settleRepairReceipt(callCtx, a, false); e != nil {
				return &repairOutcomeError{cause: e}
			}
			return &ownershipSendError{cause: err}
		}
		msg, err := b.api.EditMessageText(callCtx, p)
		ack := err == nil && msg != nil && msg.ID == c.MessageID && msg.Chat.ID == c.ChatID
		notModified := errors.Is(err, bot.ErrorBadRequest) && isNotModified(err)
		if notModified {
			ack = true
		}
		if !ack && (err == nil || !definiteTopicRefusal(err)) {
			return &repairOutcomeError{cause: errors.Join(store.ErrControlRepairPending, err)}
		}
		if e := b.settleRepairReceipt(callCtx, a, ack); e != nil {
			return &repairOutcomeError{cause: e}
		}
		if ack {
			return nil
		}
		return err
	})
}
func (b *Bot) settleRepairReceipt(ctx context.Context, a store.ControlRepairAttempt, ack bool) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := b.store.SettleControlRepair(settle, a, ack)
	if err != nil {
		b.receiptMu.Lock()
		defer b.receiptMu.Unlock()
		if b.repairReceipts == nil {
			b.repairReceipts = make(map[string]repairReceipt)
		}
		if len(b.repairReceipts) < messageReceiptLimit {
			b.repairReceipts[a.ID] = repairReceipt{a, ack}
		}
	}
	return err
}

// Reconciliation performs only DB writes from retained exact positive/refusal
// evidence. A restart or full map leaves pending rows held, never inferred absent.
func (b *Bot) reconcileRepairReceipts(ctx context.Context, match func(repairReceipt) bool) error {
	if b.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b.receiptMu.Lock()
	pending := make([]repairReceipt, 0, len(b.repairReceipts))
	for _, r := range b.repairReceipts {
		if match != nil && !match(r) {
			continue
		}
		pending = append(pending, r)
	}
	b.receiptMu.Unlock()
	var result error
	for _, r := range pending {
		err := b.store.SettleControlRepair(ctx, r.attempt, r.ack)
		if err == nil {
			b.receiptMu.Lock()
			if b.repairReceipts[r.attempt.ID] == r {
				delete(b.repairReceipts, r.attempt.ID)
			}
			b.receiptMu.Unlock()
		}
		result = errors.Join(result, err)
	}
	return result
}
