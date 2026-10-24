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

// draftRefresh is how long an unchanged draft is left before it is
// re-sent; Telegram drops a draft about 30 seconds after its last update.
const draftRefresh = 20 * time.Second

// runAgentStreaming is the streaming variant of runAgentSync: it starts
// the agent and hands the stream to streamReply.
//
// Falls back to runAgentSync if streaming setup fails (RunStream error).
func (b *Bot) runAgentStreaming(ctx context.Context, item queue.Item) {
	tier := runner.TierRegistered
	if item.IsOwner {
		tier = runner.TierOwner
	}

	stopTyping := b.startTyping(ctx, item.ChatID)
	defer stopTyping()

	prompt := item.Text
	if item.Resumed {
		prompt = resumePrompt(item.Text)
	}
	stream, err := b.runner.RunStream(ctx, item.Folder, tier, b.allowlist, prompt)
	if err != nil {
		// Fallback: if streaming setup fails, try sync path.
		b.log.Warn("RunStream failed, falling back to sync",
			slog.String("folder", item.Folder),
			slog.Any("err", err))
		b.runAgentSync(ctx, item)
		return
	}
	b.streamReply(ctx, item, stream)
}

// streamReply shows the agent's partial output while it runs and then
// delivers the final reply.
//
// Preferred path: sendMessageDraft. The draft is a preview with no
// message id; every tick it is updated with the text so far, and the
// final reply goes out once through sendFormatted, whose message id is
// the turn's reply id (D020). Nothing is memoized as a placeholder, so a
// turn resumed after a crash sends a fresh final message.
//
// Fallback path (group chats, a chat where the Bot API refused drafts,
// or a resumed turn whose interrupted attempt left a placeholder): send
// a placeholder message and edit it every tick, as before drafts.
func (b *Bot) streamReply(ctx context.Context, item queue.Item, stream *runner.AgentStream) {
	const editLimit = 4000 // safe limit under Telegram's 4096

	// startPlaceholder switches the turn to the edit path. Returns false
	// when no placeholder could be sent.
	placeholder := 0
	startPlaceholder := func() bool {
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.MessageID)
		if placeholder == 0 {
			return false
		}
		b.recordPlaceholder(ctx, item, placeholder)
		return true
	}

	draftID := 0 // non-zero while the turn streams through drafts
	var lastDraft time.Time
	if placeholder = b.reusePlaceholder(ctx, item); placeholder != 0 {
		b.recordPlaceholder(ctx, item, placeholder)
	} else if b.draftsSupported(item.ChatID) {
		// The opening draft shows the cursor and probes support up front.
		draftID = b.newDraftID()
		opening := "❘" // ▍ cursor
		if item.Resumed {
			opening = resumingBanner
		}
		if err := b.sendDraft(ctx, item.ChatID, 0, draftID, opening); err == nil {
			lastDraft = time.Now()
		} else if !b.draftsSupported(item.ChatID) || errors.Is(err, bot.ErrorBadRequest) {
			// Refused, or a bad call: edit path for this turn.
			draftID = 0
		} else {
			b.log.Debug("opening draft failed, will retry",
				slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
		}
	}
	if draftID == 0 && placeholder == 0 && !startPlaceholder() {
		b.drainAndSend(ctx, item, stream)
		return
	}

	tick := b.streamTick
	if tick <= 0 {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var accumulated strings.Builder
	dirty := false    // accumulated has text not yet shown
	overflow := false // the reply outgrew one message

loop:
	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				break loop // channel closed, agent exited
			}
			accumulated.WriteString(chunk)
			dirty = true

			if accumulated.Len() <= editLimit {
				continue
			}
			overflow = true
			if draftID != 0 {
				// A draft cannot hold more. Continue in a fresh draft
				// (replaced, not animated); the final message carries
				// the whole reply.
				draftID = b.newDraftID()
				accumulated.Reset()
				continue
			}
			// Edit path: finalize this placeholder, continue in a new one.
			b.editMessage(ctx, item.ChatID, placeholder, accumulated.String())
			placeholder = b.sendPlaceholder(ctx, item.ChatID, 0)
			if placeholder == 0 {
				b.drainAndSend(ctx, item, stream)
				return
			}
			b.recordPlaceholder(ctx, item, placeholder)
			accumulated.Reset()
			dirty = false

		case <-ticker.C:
			if draftID != 0 {
				if !dirty && time.Since(lastDraft) < draftRefresh {
					continue
				}
				text := accumulated.String() + "❘" // ▍ cursor, never empty
				err := b.sendDraft(ctx, item.ChatID, 0, draftID, text)
				if err == nil {
					lastDraft = time.Now()
					dirty = false
					continue
				}
				if b.draftsSupported(item.ChatID) {
					// Transient (rate limit, network): retry next tick.
					b.log.Debug("sendDraft failed",
						slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
					continue
				}
				// Refused: the rest of the turn uses the edit path.
				draftID = 0
				if !startPlaceholder() {
					b.drainAndSend(ctx, item, stream)
					return
				}
			}
			if dirty && accumulated.Len() > 0 {
				b.editMessage(ctx, item.ChatID, placeholder, accumulated.String()+"❘")
				dirty = false
			}

		case <-ctx.Done():
			return
		}
	}

	res := <-stream.Result
	if res.Err != nil {
		if placeholder != 0 {
			b.editMessage(ctx, item.ChatID, placeholder, "")
		}
		b.handleStreamError(ctx, item, res.Err)
		return
	}

	finalText := res.FullText
	if finalText == "" {
		finalText = "(empty reply)"
	}

	switch {
	case draftID != 0:
		// The draft is only a preview: persist the reply as a real
		// message. Sending it also clears the draft.
		sent := b.sendFormatted(ctx, item.ChatID, item.MessageID, finalText)
		b.recordReply(ctx, item, sent, finalText)
	case !overflow:
		// Everything fits in the placeholder.
		b.editMessageHTML(ctx, item.ChatID, placeholder, markdownToTelegramHTML(finalText))
		b.recordReply(ctx, item, placeholder, finalText)
	default:
		// Overflow: finalize the last placeholder with the remaining
		// text, then send the full formatted response as a new reply.
		if accumulated.Len() > 0 {
			b.editMessage(ctx, item.ChatID, placeholder, accumulated.String())
		}
		sent := b.replyTo(ctx, item.ChatID, item.MessageID, finalText)
		b.recordReply(ctx, item, sent, finalText)
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
	sent := b.replyTo(ctx, item.ChatID, item.MessageID, text)
	b.recordReply(ctx, item, sent, text)
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
