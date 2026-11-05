package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/go-telegram/bot"
)

type topicMessageKey struct {
	chat    int64
	message int
}

var ErrTopicTransportUncertain = errors.New("topic transport outcome requires reconciliation")

func topicOrigin(ctx context.Context, chat int64, thread int) chatThread {
	if origin, ok := ctx.Value(topicOutputOriginKey{}).(chatThread); ok && origin.chat == chat && thread == 0 {
		return origin
	}
	return chatThread{chat, thread}
}

// topicRequest owns a transport lease until the API has returned. Sealing a
// topic waits for these leases without turning cancellation into non-delivery.
func (b *Bot) topicRequest(ctx context.Context, chat int64, thread int, request func(context.Context) error) error {
	origin := topicOrigin(ctx, chat, thread)
	if err := b.topicAdmission(ctx, chat, thread); err != nil {
		return err
	}
	if origin.thread <= 0 || origin.chat <= 0 {
		return request(ctx)
	}
	ctx, release, err := b.topicTransport.Enter(ctx, strconv.FormatInt(origin.chat, 10), origin.thread)
	if err != nil {
		return err
	}
	defer release()
	if err = ctx.Err(); err != nil {
		return err
	}
	err = request(ctx)
	var ownershipErr *ownershipSendError
	if errors.As(err, &ownershipErr) && (!ownershipErr.attempted || ownershipErr.receiptBacked) {
		return err
	}
	if err != nil && !definiteTopicRefusal(err) {
		b.topicMu.Lock()
		if b.topicUncertain == nil {
			b.topicUncertain = make(map[chatThread]bool)
		}
		b.topicUncertain[origin] = true
		b.topicMu.Unlock()
	}
	return err
}
func definiteTopicRefusal(err error) bool {
	var rate *bot.TooManyRequestsError
	return errors.As(err, &rate) || errors.Is(err, bot.ErrorBadRequest) || errors.Is(err, bot.ErrorForbidden) || errors.Is(err, bot.ErrorUnauthorized) || errors.Is(err, bot.ErrorNotFound)
}
func (b *Bot) rememberTopicMessage(ctx context.Context, chat int64, thread, message int) {
	if message <= 0 {
		return
	}
	origin := topicOrigin(ctx, chat, thread)
	b.topicMu.Lock()
	defer b.topicMu.Unlock()
	if b.topicMessages == nil {
		b.topicMessages = make(map[topicMessageKey]chatThread)
	}
	key := topicMessageKey{chat, message}
	if _, exists := b.topicMessages[key]; !exists {
		const maxTopicMessageOwners = 4096
		if len(b.topicMessageOrder) >= maxTopicMessageOwners {
			delete(b.topicMessages, b.topicMessageOrder[0])
			b.topicMessageOrder = b.topicMessageOrder[1:]
		}
		b.topicMessageOrder = append(b.topicMessageOrder, key)
	}
	b.topicMessages[key] = origin
}
func (b *Bot) topicEditRequest(ctx context.Context, chat int64, message int, request func(context.Context) error) error {
	origin, known, ownerErr := b.messageOwner(ctx, chat, message)
	if ownerErr != nil {
		return ownerErr
	}

	supplied, hasSource := ctx.Value(topicOutputOriginKey{}).(chatThread)
	if hasSource && supplied.chat == chat {
		if !known && supplied.thread == 0 {
			if err := b.unknownMainControlAllowed(ctx, chat); err != nil {
				return err
			}
		}
		if known && origin != supplied {
			return fmt.Errorf("edit source conversation mismatch")
		}
		origin = supplied
		known = true
	}

	if !known {
		if chat <= 0 {
			return request(ctx)
		}
		ctx, release, err := b.topicTransport.Enter(ctx, strconv.FormatInt(chat, 10), -1)
		if err != nil {
			return err
		}
		defer release()
		if err := b.unknownMainControlAllowed(ctx, chat); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		err = request(ctx)
		if err != nil && !definiteTopicRefusal(err) {
			b.topicMu.Lock()
			if b.topicUncertain == nil {
				b.topicUncertain = make(map[chatThread]bool)
			}
			b.topicUncertain[chatThread{chat, -1}] = true
			b.topicMu.Unlock()
		}
		return err
	}

	ctx = withTopicOutputOrigin(ctx, origin.chat, origin.thread)
	return b.topicRequest(ctx, chat, origin.thread, request)
}
