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

// ownedTextSend preserves the existing text API through the shared creation
// journal. Payload preparation remains with each caller.
func (b *Bot) ownedTextSend(ctx context.Context, chat int64, source int, p *bot.SendMessageParams) (*models.Message, error) {
	return b.ownedMessageSend(ctx, chat, source, p.MessageThreadID, func(call context.Context) (*models.Message, error) {
		return b.api.SendMessage(call, p)
	})
}

// ownedMessageSend journals registered-private sends before HTTP, including
// topic targets and existing main-chat fallback. The source and effective target
// remain bound to the attempt across the HTTP/receipt crash gap. It neither
// authorizes new recipients nor prepares/reopens artifact data.
func (b *Bot) ownedMessageSend(ctx context.Context, chat int64, source, target int, send func(context.Context) (*models.Message, error)) (sent *models.Message, err error) {
	origin := topicOrigin(ctx, chat, source)
	err = b.topicRequest(ctx, chat, source, func(callCtx context.Context) error {
		var owner store.TelegramMessageOwner
		if b.store != nil && chat > 0 {
			var e error
			owner, e = b.store.BeginTelegramMessageOwner(callCtx, chat, origin.thread, target)
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
		sent, e = send(callCtx)
		if owner.AttemptID == "" {
			return e
		}
		settle, cancel := context.WithTimeout(context.WithoutCancel(callCtx), 5*time.Second)
		defer cancel()
		if e != nil {
			if b.topicDeletionEnabled && chat == b.ownerChatID && owner.TargetThread == owner.SourceThread && owner.SourceThread > 0 && isExactTopicDeleted(e) {
				// The source registration was captured before HTTP. Atomically
				// refuse this send and record the observation; do not resolve a
				// new registration or let an outer caller reroute into main.
				observed, observedErr := b.store.ObserveDeletedTelegramTopic(settle, owner)
				if observedErr != nil {
					return &ownershipSendError{attempted: true, cause: errors.Join(e, observedErr)}
				}
				// Installation seals/cancels but never waits for our current
				// transport lease. The worker joins it after this call returns.
				_, fenceErr := b.installTopicFence(observed, false)
				return &ownershipSendError{attempted: true, receiptBacked: true, cause: errors.Join(e, fenceErr)}
			}
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
		if sent == nil || sent.ID <= 0 || sent.Chat.ID != chat {
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
