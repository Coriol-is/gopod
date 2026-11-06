package telegram

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// ownershipSendError cannot be classified as parse/keyboard/thread refusal.
// It prevents retries of this operation; independent later output remains allowed.
type ownershipSendError struct {
	cause         error
	attempted     bool
	receiptBacked bool
}

func (e *ownershipSendError) Error() string {
	return "telegram message ownership requires reconciliation"
}
func (e *ownershipSendError) Unwrap() error  { return e.cause }
func (e *ownershipSendError) NoReplay() bool { return true }

// ownedTextSend journals registered-private text sends before HTTP, including
// topic targets and main-chat fallback. The source and effective target remain
// bound to the attempt across the HTTP/receipt crash gap.
func (b *Bot) ownedTextSend(ctx context.Context, chat int64, source int, p *bot.SendMessageParams) (sent *models.Message, err error) {
	origin := topicOrigin(ctx, chat, source)
	err = b.topicRequest(ctx, chat, source, func(callCtx context.Context) error {
		var owner store.TelegramMessageOwner
		if b.store != nil && chat > 0 {
			var e error
			owner, e = b.store.BeginTelegramMessageOwner(callCtx, chat, origin.thread, p.MessageThreadID)
			if e != nil {
				return &ownershipSendError{cause: e}
			}
		}
		if e := callCtx.Err(); e != nil {
			if owner.AttemptID != "" {
				settle, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
				defer cancel()
				if x := b.store.RefuseTelegramMessageOwner(settle, owner.AttemptID); x != nil {
					return &ownershipSendError{cause: errors.Join(e, x)}
				}
			}
			return e
		}
		var e error
		sent, e = b.api.SendMessage(callCtx, p)
		if owner.AttemptID == "" {
			return e
		}
		settle, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
		defer cancel()
		if e != nil {
			if !definiteTopicRefusal(e) {
				return &ownershipSendError{attempted: true, cause: e}
			}
			if definiteTopicRefusal(e) {
				if x := b.store.RefuseTelegramMessageOwner(settle, owner.AttemptID); x != nil {
					return &ownershipSendError{attempted: true, cause: errors.Join(e, x)}
				}
			}
			return e
		}
		if sent == nil || sent.ID <= 0 {
			return &ownershipSendError{attempted: true, cause: store.ErrMessageOwnership}
		}
		if e = b.store.AcknowledgeTelegramMessageOwner(settle, owner, sent.ID); e != nil {
			retained := b.retainMessageReceipt(owner, sent.ID)
			return &ownershipSendError{attempted: true, receiptBacked: retained, cause: e}
		}
		return nil
	})
	if err == nil && sent != nil {
		b.rememberTopicMessage(ctx, chat, source, sent.ID)
	}
	return
}

// messageOwner validates persisted scope before the cache or current callback
// context can supply authority. An old registration cannot reuse an old control.
func (b *Bot) messageOwner(ctx context.Context, chat int64, message int) (chatThread, bool, error) {
	if b.store != nil && chat > 0 {
		if err := b.reconcileMessageReceipts(ctx, func(r messageReceipt) bool { return r.owner.ChatID == chat && r.message == message }); err != nil {
			return chatThread{}, false, err
		}
		owner, err := b.store.TelegramMessageOwner(ctx, chat, message)
		if err == nil {
			return chatThread{chat, owner.SourceThread}, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return chatThread{}, false, err
		}
		control, err := b.store.GetControlMessage(ctx, chat, message)
		if err == nil {
			if err = b.store.ValidateScope(ctx, control.Scope); err != nil {
				return chatThread{}, false, err
			}
			return chatThread{chat, control.ThreadID}, true, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return chatThread{}, false, err
		}
	}
	b.topicMu.Lock()
	defer b.topicMu.Unlock()
	owner, known := b.topicMessages[topicMessageKey{chat, message}]
	return owner, known, nil
}
func (b *Bot) unknownMainControlAllowed(ctx context.Context, chat int64) error {
	if b.store == nil || chat <= 0 {
		return nil
	}
	blocked, err := b.store.UnknownMainControlBlocked(ctx, chat)
	if err != nil {
		return err
	}
	if blocked {
		return &ownershipSendError{cause: store.ErrMessageOwnership}
	}
	return nil
}
