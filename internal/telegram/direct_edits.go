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

type directEditError struct{ cause error }

func (e *directEditError) Error() string {
	return "direct edit ownership or outcome requires reconciliation"
}
func (e *directEditError) Unwrap() error  { return e.cause }
func (e *directEditError) NoReplay() bool { return true }

type editReceipt struct {
	attempt store.TelegramEditAttempt
	ack     bool
	chat    int64
	thread  int
}

// Caller holds the message gate and transport lease. This helper never re-enters
// either, including HTML/plain/bare alternatives which each get a fresh attempt.
func (b *Bot) directEdit(ctx context.Context, chat int64, msg int, method string, params any, text, parse string, finalTurn int64, send func() (*models.Message, error)) error {
	if b.store == nil || chat <= 0 {
		_, err := send()
		return err
	}
	owner, covered, err := b.store.ResolveDirectEditOwner(ctx, chat, msg)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	if !covered {
		_, err = send()
		return err
	}
	raw, err := json.Marshal(params)
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
	final := false
	if c := owner.Control; c != nil && c.Terminal && method == "text" {
		final = c.Text == text && c.HTML == (parse == "HTML") || c.HTML && parse == "" && htmlToPlain(c.Text) == text
	}
	a, err := b.store.BeginTelegramEdit(ctx, store.TelegramEditRequest{ChatID: chat, MessageID: msg, SourceThread: source, Method: method, RequestHash: hex.EncodeToString(sum[:]), FinalTurn: finalTurn}, owner, final)
	if err != nil {
		return &ownershipSendError{cause: err}
	}
	if a.ID == "" {
		return &ownershipSendError{cause: store.ErrTelegramEditHeld}
	}
	settle := func(ack bool) error { return b.settleEditReceipt(ctx, editReceipt{a, ack, chat, owner.SourceThread}) }
	if err = ctx.Err(); err != nil {
		if e := settle(false); e != nil {
			return &directEditError{cause: e}
		}
		return &ownershipSendError{cause: err}
	}
	response, err := send()
	ack := err == nil && response != nil && response.ID == msg && response.Chat.ID == chat
	if errors.Is(err, bot.ErrorBadRequest) && isNotModified(err) {
		ack = true
	}
	if !ack && (err == nil || !definiteTopicRefusal(err)) {
		return &directEditError{cause: errors.Join(store.ErrTelegramEditHeld, err)}
	}
	if e := settle(ack); e != nil {
		return &directEditError{cause: e}
	}
	if ack {
		return nil
	}
	return err
}
func (b *Bot) directText(ctx context.Context, p *bot.EditMessageTextParams) error {
	chat, ok := p.ChatID.(int64)
	if !ok {
		return &ownershipSendError{cause: store.ErrInvalidConversation}
	}
	return b.directEdit(ctx, chat, p.MessageID, "text", p, p.Text, string(p.ParseMode), 0, func() (*models.Message, error) { return b.api.EditMessageText(ctx, p) })
}
func (b *Bot) directMarkup(ctx context.Context, p *bot.EditMessageReplyMarkupParams, finalTurn int64) error {
	chat, ok := p.ChatID.(int64)
	if !ok {
		return &ownershipSendError{cause: store.ErrInvalidConversation}
	}
	return b.directEdit(ctx, chat, p.MessageID, "markup", p, "", "", finalTurn, func() (*models.Message, error) { return b.api.EditMessageReplyMarkup(ctx, p) })
}
func (b *Bot) settleEditReceipt(ctx context.Context, r editReceipt) error {
	settle, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	err := b.store.SettleTelegramEdit(settle, r.attempt, r.ack)
	if err != nil {
		b.receiptMu.Lock()
		defer b.receiptMu.Unlock()
		if b.editReceipts == nil {
			b.editReceipts = make(map[string]editReceipt)
		}
		if len(b.editReceipts) < messageReceiptLimit {
			b.editReceipts[r.attempt.ID] = r
		}
	}
	return err
}
func (b *Bot) reconcileEditReceipts(ctx context.Context, match func(editReceipt) bool) error {
	if b.store == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	b.receiptMu.Lock()
	var pending []editReceipt
	for _, r := range b.editReceipts {
		if match == nil || match(r) {
			pending = append(pending, r)
		}
	}
	b.receiptMu.Unlock()
	var result error
	for _, r := range pending {
		err := b.store.SettleTelegramEdit(ctx, r.attempt, r.ack)
		if err == nil {
			b.receiptMu.Lock()
			if b.editReceipts[r.attempt.ID] == r {
				delete(b.editReceipts, r.attempt.ID)
			}
			b.receiptMu.Unlock()
		}
		result = errors.Join(result, err)
	}
	return result
}
