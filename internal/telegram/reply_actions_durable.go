package telegram

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

type replyActionView struct {
	memo    turnMemo
	durable *store.AcknowledgedReplyAction
}

// replyActionViewFor never treats missing ACK evidence as a legacy cache miss.
// In particular, stale process memory cannot authorize a pending/uncertain reply.
func (b *Bot) replyActionViewFor(ctx context.Context, q cbQuery, turn store.Turn) (replyActionView, string, error) {
	reply, err := b.store.ReplyAction(ctx, turn.ID)
	if err != nil {
		return replyActionView{}, "", fmt.Errorf("read acknowledged reply action: %w", err)
	}
	memo, _ := b.turnMemoFor(turn.ID, q.ChatID)
	if !reply.Managed {
		conv, err := b.store.SnapshotConversation(ctx, buildChatJID(q.ChatID), q.ThreadID, store.VisibilityConversation)
		if err != nil {
			return replyActionView{}, "", err
		}
		last, found, err := b.store.LatestReplySurface(ctx, conv)
		if err != nil {
			return replyActionView{}, "", err
		}
		if found && last.MessageID != int64(q.MessageID) {
			return replyActionView{}, cbToastExpired, nil
		}
		return replyActionView{memo: memo}, "", nil
	}
	if !reply.Eligible || !reply.Newest || reply.MessageID != int64(q.MessageID) || reply.Conversation.Scope.JID != buildChatJID(q.ChatID) || reply.Conversation.Scope.Folder != q.Chat.Folder || reply.Conversation.ThreadID != q.ThreadID || !hasReplyActionOwner(reply.ReplyMarkup, turn.ID) {
		return replyActionView{}, cbToastExpired, nil
	}
	if origin, ok := ctx.Value(topicOutputOriginKey{}).(chatThread); ok && origin != (chatThread{q.ChatID, q.ThreadID}) {
		return replyActionView{}, cbToastExpired, nil
	}
	// A process-local row can be ignored only when durable observations prove
	// it is older. Missing history remains a hold rather than guessed order.
	latest, ordered, err := b.store.LatestReplySurface(ctx, reply.Conversation)
	if err != nil {
		return replyActionView{}, "", err
	}
	b.rowMu.Lock()
	last := b.rows.last[chatThread{q.ChatID, q.ThreadID}]
	b.rowMu.Unlock()
	if last.msgID != 0 && last.msgID != q.MessageID {
		seq, found, err := b.store.ReplySurfaceSequence(ctx, reply.Conversation, int64(last.msgID))
		if err != nil {
			return replyActionView{}, "", err
		}
		if !ordered || !found || seq >= latest.Sequence {
			return replyActionView{}, cbToastExpired, nil
		}
	}
	memo.reply = reply.Text
	return replyActionView{memo: memo, durable: &reply}, "", nil
}
func hasReplyActionOwner(raw string, turn int64) bool {
	var kb models.InlineKeyboardMarkup
	if json.Unmarshal([]byte(raw), &kb) != nil {
		return false
	}
	want := fmt.Sprintf("mo:%d", turn)
	for _, row := range kb.InlineKeyboard {
		for _, button := range row {
			if button.CallbackData == want {
				return true
			}
		}
	}
	return false
}

// durableReplyRequest retains the saved registration and exact topic through the
// transport lease. It has no topic-gone redirect and never retries HTTP. Recheck
// callback authority inside the lease so an intervening ACK/retirement is seen.
func (b *Bot) durableReplyRequest(ctx context.Context, q cbQuery, turn store.Turn, view replyActionView, send func(context.Context) error) error {
	return b.topicRequest(ctx, q.ChatID, q.ThreadID, func(callCtx context.Context) error {
		if err := b.store.ValidateConversation(callCtx, view.durable.Conversation); err != nil {
			return &ownershipSendError{cause: err, attempted: false}
		}
		current, toast, err := b.replyActionViewFor(callCtx, q, turn)
		if err != nil {
			return &ownershipSendError{cause: err, attempted: false}
		}
		if toast != "" || current.durable == nil || current.durable.Conversation != view.durable.Conversation {
			return &ownershipSendError{cause: store.ErrInvalidDelivery, attempted: false}
		}
		return send(callCtx)
	})
}
func (b *Bot) durableReplyKeyboard(ctx context.Context, q cbQuery, turn store.Turn, view replyActionView, kb *models.InlineKeyboardMarkup) error {
	release, err := b.lockMessageEdit(ctx, q.ChatID, q.MessageID)
	if err != nil {
		return err
	}
	defer release()

	return b.durableReplyRequest(ctx, q, turn, view, func(callCtx context.Context) error {
		c, err := b.store.GetControlMessage(callCtx, q.ChatID, q.MessageID)
		if err == nil {
			_, err = b.store.ControlFinalOwnership(callCtx, c)
		}
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return &ownershipSendError{cause: err}
		}
		return b.directMarkup(callCtx, &bot.EditMessageReplyMarkupParams{ChatID: q.ChatID, MessageID: q.MessageID, ReplyMarkup: kb}, turn.ID)
	})
}

// A legacy action must not erase a pending control repair's intent. A bound
// placeholder becomes an action surface only after its terminal text ACK. There
// is no keyboard retry journal: a failed action stays visible as failure, while
// the acknowledged terminal checkpoint prevents repair from clearing a good row.
func (b *Bot) legacyReplyKeyboard(ctx context.Context, q cbQuery, kb *models.InlineKeyboardMarkup) error {
	release, err := b.lockMessageEdit(ctx, q.ChatID, q.MessageID)
	if err != nil {
		return err
	}
	defer release()
	if b.store != nil {
		c, err := b.store.GetControlMessage(ctx, q.ChatID, q.MessageID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if err == nil {
			if !c.Terminal || c.Acknowledged < c.Revision || c.ThreadID != q.ThreadID {
				return store.ErrInvalidDelivery
			}
			if err = b.store.ValidateScope(ctx, c.Scope); err != nil {
				return err
			}
			held, err := b.store.ControlFinalOwnership(ctx, c)
			if err != nil {
				return err
			}
			if held {
				return store.ErrInvalidDelivery
			}
		}
	}
	return b.topicEditRequest(ctx, q.ChatID, q.MessageID, func(callCtx context.Context) error {
		return b.directMarkup(callCtx, &bot.EditMessageReplyMarkupParams{ChatID: q.ChatID, MessageID: q.MessageID, ReplyMarkup: kb}, 0)
	})
}
