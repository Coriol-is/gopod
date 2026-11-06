package telegram

import (
	"context"
	"encoding/json"
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
		return replyActionView{memo: memo}, "", nil
	}
	if !reply.Eligible || !reply.Newest || reply.MessageID != int64(q.MessageID) || reply.Conversation.Scope.JID != buildChatJID(q.ChatID) || reply.Conversation.Scope.Folder != q.Chat.Folder || reply.Conversation.ThreadID != q.ThreadID || !hasReplyActionOwner(reply.ReplyMarkup, turn.ID) {
		return replyActionView{}, cbToastExpired, nil
	}
	if origin, ok := ctx.Value(topicOutputOriginKey{}).(chatThread); ok && origin != (chatThread{q.ChatID, q.ThreadID}) {
		return replyActionView{}, cbToastExpired, nil
	}
	// The cache may know a later legacy row, but does not carry durable sequence
	// evidence. Never choose between different mixed-path rows by message number.
	b.rowMu.Lock()
	last := b.rows.last[chatThread{q.ChatID, q.ThreadID}]
	b.rowMu.Unlock()
	if last.msgID != 0 && last.msgID != q.MessageID {
		return replyActionView{}, cbToastExpired, nil
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
	return b.durableReplyRequest(ctx, q, turn, view, func(callCtx context.Context) error {
		_, err := b.api.EditMessageReplyMarkup(callCtx, &bot.EditMessageReplyMarkupParams{ChatID: q.ChatID, MessageID: q.MessageID, ReplyMarkup: kb})
		return err
	})
}
