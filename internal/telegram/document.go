package telegram

import (
	"bytes"
	"context"
	"fmt"

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
	if !b.threadGone(chatID, thread) {
		id, err := b.uploadDocument(ctx, chatID, thread, name, data, caption)
		if !b.checkThreadGone(chatID, thread, err) {
			return id, err
		}
	}
	return b.uploadDocument(ctx, chatID, 0, name, data, caption)
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
