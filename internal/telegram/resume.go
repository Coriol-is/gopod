package telegram

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/go-telegram/bot"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// resumingBanner replaces a stale streaming placeholder while a
// resumed turn runs.
const resumingBanner = "⟳ resuming after restart"

// crashLoopNotice is sent when Recover gives up on a turn.
const crashLoopNotice = "Gave up on this message after 3 restarts. Please resend."

// resumePrompt wraps the user's original text in an interruption notice.
// The session continues (`claude --continue` / `codex resume --last`),
// so the agent sees its own partial transcript and decides what is
// safe to redo. gopod does not classify tool side effects.
func resumePrompt(userText string) string {
	return "[gopod] The previous turn was interrupted by a restart before a reply was sent. " +
		"The user's message was:\n\n" + userText +
		"\n\nContinue where you left off, or reply."
}

// NewDoneHook returns a queue.OnDone hook that reacts ❌ and explains
// when a turn was abandoned as a crash loop. Every other completion is
// already handled by the agent handler itself.
func (b *Bot) NewDoneHook() func(queue.Item, error) {
	return func(it queue.Item, err error) {
		if !errors.Is(err, queue.ErrCrashLoop) || it.ChatID == 0 || b.api == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if it.MessageID > 0 {
			b.react(ctx, it.ChatID, it.MessageID, emojiError)
		}
		b.replyTo(ctx, it.ChatID, it.MessageID, crashLoopNotice)
	}
}

// recordPlaceholder memoizes the streaming placeholder id on the turn
// row. Best effort: failure is logged, the turn goes on.
func (b *Bot) recordPlaceholder(ctx context.Context, item queue.Item, msgID int) {
	if item.ID == 0 || msgID == 0 || b.store == nil {
		return
	}
	if err := b.store.SetTurnPlaceholder(ctx, item.ID, msgID); err != nil {
		b.log.Warn("record placeholder", slog.Int64("turn", item.ID), slog.Any("err", err))
	}
}

// recordReply memoizes the final reply id on the turn row and persists
// the outbound text to messages so the SQLite transcript has both
// sides. Best effort: the user already has the reply.
func (b *Bot) recordReply(ctx context.Context, item queue.Item, msgID int, text string) {
	if msgID == 0 || b.store == nil {
		return
	}
	if item.ID != 0 {
		if err := b.store.SetTurnReply(ctx, item.ID, msgID); err != nil {
			b.log.Warn("record reply", slog.Int64("turn", item.ID), slog.Any("err", err))
		}
	}
	if item.ChatID == 0 {
		return
	}
	_, err := b.store.SaveMessage(ctx, store.Message{
		ChatJID:            buildChatJID(item.ChatID),
		TGMessageID:        int64(msgID),
		Sender:             "bot",
		SenderName:         "gopod",
		Content:            text,
		Timestamp:          time.Now().UnixMilli(),
		IsFromMe:           true,
		IsBotMessage:       true,
		ReplyToTGMessageID: int64(item.MessageID),
	})
	if err != nil {
		b.log.Warn("persist outbound", slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
	}
}

// reusePlaceholder edits the placeholder left by the interrupted
// attempt to the resuming banner and returns its id, or 0 when there
// is none or the edit fails (deleted message), in which case the
// caller sends a fresh placeholder.
func (b *Bot) reusePlaceholder(ctx context.Context, item queue.Item) int {
	if !item.Resumed || item.PlaceholderMsgID == 0 || b.api == nil {
		return 0
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    item.ChatID,
		MessageID: item.PlaceholderMsgID,
		Text:      resumingBanner,
	})
	if err != nil {
		b.log.Debug("reuse placeholder failed, sending a new one",
			slog.Int64("chat_id", item.ChatID),
			slog.Int("msg_id", item.PlaceholderMsgID),
			slog.Any("err", err))
		return 0
	}
	return item.PlaceholderMsgID
}
