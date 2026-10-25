package telegram

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// sendDocument uploads data as a file named name into thread (0 = main
// chat) and returns the message id. Like sendFormattedErr it honours the
// topic-gone fallback (threadgone.go): a topic known gone, or one this
// send finds gone, gets the document in the main chat instead. The data
// is in memory, so unlike a voice reply the upload can be repeated.
// caption is plain text; keep it short.
func (b *Bot) sendDocument(ctx context.Context, chatID int64, thread int, name string, data []byte, caption string) (int, error) {
	b.chatAction(ctx, chatID, thread, models.ChatActionUploadDocument)
	if !b.threadGone(chatID, thread) {
		id, err := b.uploadDocument(ctx, chatID, thread, name, data, caption)
		if !b.checkThreadGone(chatID, thread, err) {
			return id, err
		}
	}
	return b.uploadDocument(ctx, chatID, 0, name, data, caption)
}

// chatAction shows action ("sending a file…", "recording audio…") in
// thread once; Telegram clears it after about 5 s or at the next
// message. Best effort: a failure is only logged at debug. A topic
// known gone gets it in the main chat, where the content will go.
func (b *Bot) chatAction(ctx context.Context, chatID int64, thread int, action models.ChatAction) {
	if b.threadGone(chatID, thread) {
		thread = 0
	}
	_, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{
		ChatID:          chatID,
		MessageThreadID: thread,
		Action:          action,
	})
	if err != nil {
		b.log.Debug("SendChatAction failed",
			slog.Int64("chat_id", chatID),
			slog.String("action", string(action)),
			slog.Any("err", err))
	}
}

func (b *Bot) uploadDocument(ctx context.Context, chatID int64, thread int, name string, data []byte, caption string) (int, error) {
	msg, err := b.api.SendDocument(ctx, &bot.SendDocumentParams{
		ChatID:          chatID,
		MessageThreadID: thread,
		Document:        &models.InputFileUpload{Filename: name, Data: bytes.NewReader(data)},
		Caption:         caption,
	})
	if err != nil {
		return 0, fmt.Errorf("sendDocument %s: %w", name, err)
	}
	return msg.ID, nil
}
