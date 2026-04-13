package telegram

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// sendPlaceholder sends a "▍" (blinking cursor) message and returns
// its message ID. The caller uses this ID for subsequent editMessage
// calls to stream partial output. Returns 0 if the send fails.
//
// replyToMsgID is the user's original message ID for reply threading;
// pass 0 for no threading.
func (b *Bot) sendPlaceholder(ctx context.Context, chatID int64, replyToMsgID int) int {
	params := &bot.SendMessageParams{
		ChatID: chatID,
		Text:   "\u2758", // ▍ cursor
	}
	if replyToMsgID > 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                replyToMsgID,
			AllowSendingWithoutReply: true,
		}
	}

	sent, err := b.api.SendMessage(ctx, params)
	if err != nil {
		b.log.Warn("sendPlaceholder failed",
			slog.Int64("chat_id", chatID),
			slog.Any("err", err))
		return 0
	}
	return sent.ID
}

// editMessage edits a previously sent message with plain text content.
// Errors are logged but not propagated — a failed edit means the user
// sees a slightly stale partial; the next edit or final message fixes it.
func (b *Bot) editMessage(ctx context.Context, chatID int64, msgID int, text string) {
	if text == "" {
		text = "\u2758" // keep cursor visible if empty
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      text,
	})
	if err != nil {
		b.log.Debug("editMessage failed",
			slog.Int64("chat_id", chatID),
			slog.Int("msg_id", msgID),
			slog.Any("err", err))
	}
}

// editMessageHTML edits a previously sent message with HTML-formatted
// content. Falls back to plain text if the HTML edit fails (e.g. due
// to malformed tags).
func (b *Bot) editMessageHTML(ctx context.Context, chatID int64, msgID int, html string) {
	if html == "" {
		return
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      html,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		// Fallback: try plain text (strip HTML).
		b.log.Debug("editMessageHTML failed, falling back to plain text",
			slog.Int64("chat_id", chatID),
			slog.Int("msg_id", msgID),
			slog.Any("err", err))
		b.editMessage(ctx, chatID, msgID, html)
	}
}

// runAgentStreaming is the streaming variant of runAgentSync. It sends
// a placeholder message, then edits it every ~1 second with partial
// agent output as it arrives. On completion, the final edit uses HTML
// formatting.
//
// Falls back to runAgentSync if streaming setup fails (ExecStream
// error, placeholder send failure).
func (b *Bot) runAgentStreaming(ctx context.Context, item queue.Item) {
	tier := runner.TierRegistered
	if item.IsOwner {
		tier = runner.TierOwner
	}

	stopTyping := b.startTyping(ctx, item.ChatID)
	defer stopTyping()

	stream, err := b.runner.RunStream(ctx, item.Folder, tier, b.allowlist, item.Text)
	if err != nil {
		// Fallback: if streaming setup fails, try sync path.
		b.log.Warn("RunStream failed, falling back to sync",
			slog.String("folder", item.Folder),
			slog.Any("err", err))
		b.runAgentSync(ctx, item)
		return
	}

	// Send placeholder message -> get its message_id for editing.
	placeholder := b.sendPlaceholder(ctx, item.ChatID, item.MessageID)
	if placeholder == 0 {
		// Fallback: drain stream, send as buffered.
		b.drainAndSend(ctx, item, stream)
		return
	}

	var accumulated strings.Builder
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	dirty := false       // true if accumulated has new data since last edit
	overflow := false     // true if we've exceeded single-message limit
	const editLimit = 4000 // safe limit under Telegram's 4096

	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				goto done // channel closed, agent exited
			}
			accumulated.WriteString(chunk)
			dirty = true

			// If accumulated text exceeds edit limit, flush as
			// a finalized message and start a fresh accumulator.
			if accumulated.Len() > editLimit {
				b.editMessage(ctx, item.ChatID, placeholder,
					accumulated.String())
				overflow = true
				// New placeholder for continuation (no reply threading).
				placeholder = b.sendPlaceholder(ctx, item.ChatID, 0)
				if placeholder == 0 {
					// Can't send more placeholders — drain rest silently.
					b.drainAndSend(ctx, item, stream)
					return
				}
				accumulated.Reset()
				dirty = false
			}

		case <-ticker.C:
			if dirty && accumulated.Len() > 0 {
				text := accumulated.String() + "\u2758" // ▍ cursor
				b.editMessage(ctx, item.ChatID, placeholder, text)
				dirty = false
			}

		case <-ctx.Done():
			return
		}
	}

done:
	// Final result.
	res := <-stream.Result
	if res.Err != nil {
		b.editMessage(ctx, item.ChatID, placeholder, "")
		b.handleStreamError(ctx, item, res.Err)
		return
	}

	finalText := res.FullText
	if finalText == "" {
		finalText = "(empty reply)"
	}

	// Final edit with properly formatted text.
	if !overflow {
		// Simple case: everything fits in one message.
		html := markdownToTelegramHTML(finalText)
		b.editMessageHTML(ctx, item.ChatID, placeholder, html)
	} else {
		// Overflow: finalize last placeholder with remaining text,
		// then send the full formatted response as a new reply.
		if accumulated.Len() > 0 {
			b.editMessage(ctx, item.ChatID, placeholder,
				accumulated.String())
		}
		b.replyTo(ctx, item.ChatID, item.MessageID, finalText)
	}

	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}

	// Voice reply (must wait for full text).
	b.maybeVoiceReply(ctx, item, finalText)
}

// handleStreamError handles errors from a streaming agent run.
// Mirrors the error classification in runAgentSync.
func (b *Bot) handleStreamError(ctx context.Context, item queue.Item, err error) {
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiError)
	}
	if errors.Is(err, runner.ErrSessionExpired) {
		b.replyTo(ctx, item.ChatID, item.MessageID,
			"Session expired. Run /login to re-authenticate.")
		return
	}
	if errors.Is(err, runner.ErrNotLoggedIn) {
		b.replyTo(ctx, item.ChatID, item.MessageID,
			"Not authenticated. Run /login to sign in.")
		return
	}
	b.log.Error("streaming agent run failed",
		slog.String("chat_folder", item.Folder),
		slog.Any("err", err))
	b.replyTo(ctx, item.ChatID, item.MessageID,
		"Something went wrong. Check gopod logs for details.")
}

// drainAndSend is the fallback path: drain a streaming AgentStream
// into a buffer and send the result as a single formatted message.
// Used when the placeholder can't be sent.
func (b *Bot) drainAndSend(ctx context.Context, item queue.Item, stream *runner.AgentStream) {
	// Drain remaining chunks.
	for range stream.Chunks {
	}
	res := <-stream.Result
	if res.Err != nil {
		b.handleStreamError(ctx, item, res.Err)
		return
	}
	text := res.FullText
	if text == "" {
		text = "(empty reply)"
	}

	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}
	b.replyTo(ctx, item.ChatID, item.MessageID, text)
	b.maybeVoiceReply(ctx, item, text)
}

// maybeVoiceReply sends a TTS voice reply if the chat's reply mode
// includes voice. Extracted here to share between sync and streaming paths.
func (b *Bot) maybeVoiceReply(ctx context.Context, item queue.Item, text string) {
	mode := b.getReplyMode(item.ChatID, item.IsVoice)
	if mode != "voice" && mode != "voice+text" {
		return
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return
	}
	audioData, err := textToSpeech(ctx, apiKey, text)
	if err != nil {
		b.log.Warn("TTS failed",
			slog.Any("err", err))
		return
	}
	if err := b.sendVoiceReply(ctx, item.ChatID, item.MessageID, audioData); err != nil {
		b.log.Warn("send voice reply failed",
			slog.Any("err", err))
	}
}
