package telegram

import (
	"context"
	"errors"
	"fmt"
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
// thread is the private-chat topic (0 = main chat). replyToMsgID is
// the user's original message ID for reply threading; pass 0 for no
// threading.
func (b *Bot) sendPlaceholder(ctx context.Context, chatID int64, thread int, replyToMsgID int) int {
	if b.threadGone(chatID, thread) {
		return 0 // the caller falls back to one final message, sent to main
	}
	params := &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: thread,
		Text:            "\u2758", // ▍ cursor
	}
	if replyToMsgID > 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                replyToMsgID,
			AllowSendingWithoutReply: true,
		}
	}

	sent, err := b.api.SendMessage(ctx, params)
	if err != nil {
		if b.checkThreadGone(chatID, thread, err) {
			return 0
		}
		b.log.Warn("sendPlaceholder failed",
			slog.Int64("chat_id", chatID),
			slog.Any("err", err))
		return 0
	}
	return sent.ID
}

// editMessage edits a previously sent message with plain text content.
// Errors are logged and returned; mid-stream callers ignore them — a
// failed edit means the user sees a slightly stale partial; the next
// edit or final message fixes it.
func (b *Bot) editMessage(ctx context.Context, chatID int64, msgID int, text string) error {
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
		return fmt.Errorf("editMessageText: %w", err)
	}
	return nil
}

// editMessageHTML edits a previously sent message with HTML-formatted
// content. Falls back to plain text if the HTML edit fails (e.g. due
// to malformed tags). Returns the error of the last attempt.
func (b *Bot) editMessageHTML(ctx context.Context, chatID int64, msgID int, html string) error {
	if html == "" {
		return nil
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:    chatID,
		MessageID: msgID,
		Text:      html,
		ParseMode: models.ParseModeHTML,
	})
	if err != nil {
		if isNotModified(err) {
			// The message already shows this text: nothing to fall back to.
			return fmt.Errorf("editMessageText: %w", err)
		}
		// Fallback: plain text, tags stripped and entities unescaped, so
		// the user never sees raw markup.
		b.log.Debug("editMessageHTML failed, falling back to plain text",
			slog.Int64("chat_id", chatID),
			slog.Int("msg_id", msgID),
			slog.Any("err", err))
		return b.editMessage(ctx, chatID, msgID, htmlToPlain(html))
	}
	return nil
}

// draftRefresh is how long an unchanged draft is left before it is
// re-sent; Telegram drops a draft about 30 seconds after its last update.
const draftRefresh = 20 * time.Second

// runAgentStreaming is the streaming variant of runAgentSync: it starts
// the agent and hands the stream to streamReply.
//
// The agent runs on a child context registered as in-flight, so the
// Stop button cancels this turn's run and nothing else; Telegram calls
// keep using ctx. Returns queue.ErrStopped when the user stopped the
// turn, nil otherwise.
//
// Falls back to runAgentSync if streaming setup fails (RunStream error).
func (b *Bot) runAgentStreaming(ctx context.Context, item queue.Item) error {
	tier := runner.TierRegistered
	if item.IsOwner {
		tier = runner.TierOwner
	}

	stopTyping := b.startTyping(ctx, item.ChatID, item.ThreadID)
	defer stopTyping()

	prompt := item.Text
	if item.Resumed {
		prompt = resumePrompt(item.Text)
	}
	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	defer b.registerInflight(item.ID, cancelRun)()

	stream, err := b.runner.RunStream(runCtx, item.Folder, item.ThreadID, tier, b.allowlist, prompt)
	if err != nil {
		// Fallback: if streaming setup fails, try sync path.
		b.log.Warn("RunStream failed, falling back to sync",
			slog.String("folder", item.Folder),
			slog.Any("err", err))
		b.runAgentSync(ctx, item)
		return nil
	}
	return b.streamReply(ctx, item, stream)
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
//
// Stop: when the turn is registered in-flight (registerInflight), its
// drafts carry can_stop and keep_on_stop. A stop ends the turn with the
// text so far as the reply (see finishStopped) and returns
// queue.ErrStopped. Otherwise streamReply returns nil.
func (b *Bot) streamReply(ctx context.Context, item queue.Item, stream *runner.AgentStream) error {
	const editLimit = 4000 // safe limit under Telegram's 4096

	stop := b.inflightStop(item.ID) // nil: not stoppable, never fires
	stopOpts := draftOpts{CanStop: stop != nil, KeepOnStop: stop != nil}
	// useDraft makes id the turn's current draft, the one a stop names.
	useDraft := func(id int) int {
		b.bindDraft(item.ID, item.ChatID, id)
		return id
	}

	// startPlaceholder switches the turn to the edit path. Returns false
	// when no placeholder could be sent.
	placeholder := 0
	startPlaceholder := func() bool {
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.ThreadID, item.MessageID)
		if placeholder == 0 {
			return false
		}
		b.recordPlaceholder(ctx, item, placeholder)
		return true
	}

	draftID := 0 // non-zero while the turn streams through drafts
	var lastDraft time.Time
	var draftHold time.Time // no draft updates before this (429 retry_after)
	if placeholder = b.reusePlaceholder(ctx, item); placeholder != 0 {
		b.recordPlaceholder(ctx, item, placeholder)
	} else if b.draftsSupported(item.ChatID) {
		// The opening draft shows the cursor and probes support up front.
		draftID = useDraft(b.newDraftID())
		opening := "❘" // ▍ cursor
		if item.Resumed {
			opening = resumingBanner
		}
		if err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, draftID, opening, stopOpts); err == nil {
			lastDraft = time.Now()
		} else if !b.draftsSupported(item.ChatID) || errors.Is(err, bot.ErrorBadRequest) {
			// Refused, or a bad call: edit path for this turn.
			draftID = useDraft(0)
		} else {
			b.log.Debug("opening draft failed, will retry",
				slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
		}
	}
	if draftID == 0 && placeholder == 0 && !startPlaceholder() {
		b.drainAndSend(ctx, item, stream)
		return nil
	}

	tick := b.streamTick
	if tick <= 0 {
		tick = time.Second
	}
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	var accumulated strings.Builder // text of the current draft/placeholder
	var full strings.Builder        // every chunk so far, for a stop
	dirty := false                  // the view has changed since last shown
	overflow := false               // the reply outgrew one message

	// Structured runs (stream.Progress != nil) also report tool steps.
	// Until answer text arrives after the latest step, the view is the
	// last progressShown steps instead of the text.
	var steps []string
	answering := false // answer text arrived since the latest step
	view := func() string {
		if !answering && len(steps) > 0 {
			return progressView(steps)
		}
		return accumulated.String()
	}

	// addChunk applies one answer chunk. Returns false when the turn
	// had to be finished by drainAndSend.
	addChunk := func(chunk string) bool {
		accumulated.WriteString(chunk)
		full.WriteString(chunk)
		answering = true
		dirty = true

		if accumulated.Len() <= editLimit {
			return true
		}
		overflow = true
		if draftID != 0 {
			// A draft cannot hold more. Continue in a fresh draft
			// (replaced, not animated); the final message carries
			// the whole reply.
			draftID = useDraft(b.newDraftID())
			accumulated.Reset()
			return true
		}
		// Edit path: finalize this placeholder, continue in a new one.
		b.editMessage(ctx, item.ChatID, placeholder, accumulated.String())
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.ThreadID, 0)
		if placeholder == 0 {
			b.drainAndSend(ctx, item, stream)
			return false
		}
		b.recordPlaceholder(ctx, item, placeholder)
		accumulated.Reset()
		dirty = false
		return true
	}

	progress := stream.Progress // nil for plain runs: never selected
loop:
	for {
		select {
		case chunk, ok := <-stream.Chunks:
			if !ok {
				break loop // channel closed, agent exited
			}
			if !addChunk(chunk) {
				return nil
			}

		case ev, ok := <-progress:
			if !ok {
				progress = nil
				continue
			}
			// The runner sent every chunk that precedes this step
			// before it (Progress is unbuffered): apply those first.
		drain:
			for {
				select {
				case chunk, ok := <-stream.Chunks:
					if !ok {
						break drain // seen again by the outer select
					}
					if !addChunk(chunk) {
						return nil
					}
				default:
					break drain
				}
			}
			// Text before a tool step was narration: the answer (and
			// what a stop delivers) starts again after it.
			steps = append(steps, progressLine(ev))
			if len(steps) > progressShown {
				steps = steps[len(steps)-progressShown:]
			}
			answering = false
			accumulated.Reset()
			full.Reset()
			overflow = false // the current draft/placeholder holds no answer yet
			dirty = true

		case <-ticker.C:
			if draftID != 0 {
				if !dirty && time.Since(lastDraft) < draftRefresh {
					continue
				}
				if time.Now().Before(draftHold) {
					continue // rate limited: wait out retry_after
				}
				text := view() + "❘" // ▍ cursor, never empty
				err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, draftID, text, stopOpts)
				if err == nil {
					lastDraft = time.Now()
					dirty = false
					continue
				}
				if wait, ok := retryAfter(err); ok {
					draftHold = time.Now().Add(wait)
				}
				if b.draftsSupported(item.ChatID) && !errors.Is(err, errThreadGone) {
					// Transient (rate limit, network): retry next tick.
					b.log.Debug("sendDraft failed",
						slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
					continue
				}
				// Refused, or the topic is gone: the rest of the turn
				// uses the edit path (which, for a gone topic, sends no
				// placeholder and ends in one reply to main).
				draftID = useDraft(0)
				if !startPlaceholder() {
					b.drainAndSend(ctx, item, stream)
					return nil
				}
			}
			if v := view(); dirty && v != "" {
				b.editMessage(ctx, item.ChatID, placeholder, v+"❘")
				dirty = false
			}

		case <-stop:
			return b.finishStopped(ctx, item, stream, full.String())

		case <-ctx.Done():
			return nil
		}
	}

	// A stop can race the end of the stream: select picks at random
	// when Chunks is closed and stop is ready, and a stop can land while
	// waiting for Result. Either way the stop already cancelled the run,
	// so the turn ends stopped with the text the user saw, never done.
	var res runner.RunResult
	select {
	case <-stop:
		return b.finishStopped(ctx, item, stream, full.String())
	default:
	}
	select {
	case res = <-stream.Result:
	case <-stop:
		return b.finishStopped(ctx, item, stream, full.String())
	}
	select {
	case <-stop:
		return b.finishStopped(ctx, item, stream, full.String())
	default:
	}

	if res.Err != nil {
		if placeholder != 0 {
			b.editMessage(ctx, item.ChatID, placeholder, "")
		}
		b.handleStreamError(ctx, item, res.Err)
		return nil
	}

	finalText := res.FullText
	if finalText == "" {
		finalText = "(empty reply)"
	}

	// The topic vanished after the placeholder went out: editing it
	// would deliver nothing, so send the reply (to main) like a draft turn.
	gone := placeholder != 0 && b.threadGone(item.ChatID, item.ThreadID)

	switch {
	case draftID != 0 || gone:
		// The draft is only a preview: persist the reply as a real
		// message. Sending it also clears the draft. The draft expires
		// on its own, so an undelivered reply must not end the turn
		// done: return an error and let the queue retry the turn.
		sent, err := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, finalText)
		if sent == 0 {
			b.log.Error("final reply undelivered",
				slog.Int64("turn", item.ID),
				slog.Int64("chat_id", item.ChatID),
				slog.Any("err", err))
			return fmt.Errorf("%w: %w", errReplyUndelivered, err)
		}
		if err != nil {
			// Some chunks went out: record them, never resend them.
			b.log.Warn("final reply partly delivered",
				slog.Int64("turn", item.ID), slog.Any("err", err))
		}
		b.recordReply(ctx, item, sent, finalText)
	case !overflow:
		// Everything fits in the placeholder. Oversized code goes out as
		// documents first; the reply in main keeps their notes.
		shown := b.attachLongCode(ctx, item.ChatID, item.ThreadID, finalText)
		err := b.editMessageHTML(ctx, item.ChatID, placeholder, finalReplyHTML(shown))
		if b.finalEditLost(item, err) {
			if err := b.replyInMain(ctx, item, shown); err != nil {
				return err
			}
			break
		}
		b.recordReply(ctx, item, placeholder, finalText)
	default:
		// Overflow: finalize the last placeholder with the remaining
		// text, then send the full formatted response as a new reply.
		if accumulated.Len() > 0 {
			err := b.editMessage(ctx, item.ChatID, placeholder, accumulated.String())
			if b.finalEditLost(item, err) {
				if err := b.replyInMain(ctx, item, finalText); err != nil {
					return err
				}
				break
			}
		}
		sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, finalText)
		b.recordReply(ctx, item, sent, finalText)
	}

	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}

	// Voice reply (must wait for full text).
	b.maybeVoiceReply(ctx, item, finalText)
	return nil
}

// finalEditLost reports whether the final edit of a turn's placeholder
// in a topic failed, so the reply may never have reached the user: the
// topic can be deleted after the placeholder went out, and an edit
// there fails ("message to edit not found" or a thread-gone error). A
// "message is not modified" error is benign. The thread is marked gone
// when the error says so. In the main chat (thread 0) an edit failure
// stays only logged, as before topics existed.
func (b *Bot) finalEditLost(item queue.Item, err error) bool {
	if item.ThreadID == 0 || err == nil || isNotModified(err) {
		return false
	}
	b.checkThreadGone(item.ChatID, item.ThreadID, err)
	b.log.Warn("final edit in topic failed, replying in the main chat",
		slog.Int64("turn", item.ID),
		slog.Int64("chat_id", item.ChatID),
		slog.Int("thread", item.ThreadID),
		slog.Any("err", err))
	return true
}

// isNotModified reports the Bot API's benign edit error for a message
// whose new content equals the old.
func isNotModified(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "message is not modified")
}

// replyInMain delivers a turn's final text to the main chat, prefixed
// with threadGoneNote, and records it as the turn's reply. Like the
// draft path it returns errReplyUndelivered when nothing went out, so
// the turn is not marked done with its answer lost.
func (b *Bot) replyInMain(ctx context.Context, item queue.Item, text string) error {
	sent, err := b.sendFinal(ctx, item.ChatID, 0, 0, threadGoneNote+text)
	if sent == 0 {
		b.log.Error("final reply undelivered",
			slog.Int64("turn", item.ID),
			slog.Int64("chat_id", item.ChatID),
			slog.Any("err", err))
		return fmt.Errorf("%w: %w", errReplyUndelivered, err)
	}
	if err != nil {
		b.log.Warn("final reply partly delivered",
			slog.Int64("turn", item.ID), slog.Any("err", err))
	}
	b.recordReply(ctx, item, sent, text)
	return nil
}

// errReplyUndelivered means the turn's final message could not be sent
// after bounded retries. The handler returns it so the queue does not
// mark the turn done; the queue's backoff retry runs the turn again.
var errReplyUndelivered = errors.New("telegram: final reply undelivered")

// Final-send retry bounds: attempts, and the total retry_after wait.
const (
	finalSendAttempts = 3
	finalSendMaxWait  = 30 * time.Second
)

// sendFinal sends the turn's final reply, retrying only on 429 (the
// request was refused, so nothing was delivered and a retry cannot
// duplicate) after the server's retry_after, at most finalSendAttempts
// times and finalSendMaxWait in total. Any other error, or a send that
// delivered some chunks, is returned at once. Returns the last
// delivered message id, 0 when nothing was delivered.
//
// Every path that delivers an agent's final reply goes through here:
// oversized code blocks are sent as documents first (attachLongCode),
// and the text renders with finalReplyHTML (long blocks collapsed).
func (b *Bot) sendFinal(ctx context.Context, chatID int64, thread int, replyTo int, text string) (int, error) {
	text = b.attachLongCode(ctx, chatID, thread, text)
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		id, err := b.sendRendered(ctx, chatID, thread, replyTo, text, finalReplyHTML)
		if err == nil || id != 0 {
			return id, err
		}
		wait, ok := retryAfter(err)
		if !ok || attempt >= finalSendAttempts || waited+wait > finalSendMaxWait {
			if ok {
				b.log.Warn("final reply rate limited, giving up",
					slog.Int64("chat_id", chatID),
					slog.Int("thread", thread),
					slog.Int("attempts", attempt))
			}
			return 0, err
		}
		b.log.Info("final reply rate limited, retrying",
			slog.Int64("chat_id", chatID), slog.Duration("wait", wait))
		if serr := b.wait(ctx, wait); serr != nil {
			return 0, fmt.Errorf("%w (wait: %w)", err, serr)
		}
		waited += wait
	}
}

// retryAfter reports the wait a 429 asks for (at least one second).
func retryAfter(err error) (time.Duration, bool) {
	var tm *bot.TooManyRequestsError
	if !errors.As(err, &tm) {
		return 0, false
	}
	return time.Duration(max(tm.RetryAfter, 1)) * time.Second, true
}

// wait sleeps d or until ctx ends, through b.sleep when tests set it.
func (b *Bot) wait(ctx context.Context, d time.Duration) error {
	if b.sleep != nil {
		return b.sleep(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// stoppedEmpty is the reply when the user stopped a turn before the
// agent produced any text.
const stoppedEmpty = "(stopped)"

// finishStopped ends a turn the user stopped. Stop is offered only on
// drafts, which Telegram removes once the bot sends a message
// (keep_on_stop keeps one only briefly), so the partial text is sent
// as a real message and memoized as the turn's reply. The agent's
// remaining output is drained in the background and discarded, so the
// runner's goroutine never blocks on a reader that has gone.
func (b *Bot) finishStopped(ctx context.Context, item queue.Item, stream *runner.AgentStream, partial string) error {
	go func() {
		drainStream(stream)
		<-stream.Result
	}()

	text := strings.TrimSpace(partial)
	if text == "" {
		text = stoppedEmpty
	}
	sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, text)
	b.recordReply(ctx, item, sent, text)
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}
	return queue.ErrStopped
}

// handleStreamError handles errors from a streaming agent run.
// Mirrors the error classification in runAgentSync.
func (b *Bot) handleStreamError(ctx context.Context, item queue.Item, err error) {
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiError)
	}
	if errors.Is(err, runner.ErrSessionExpired) {
		b.replyTo(ctx, item.ChatID, item.ThreadID, item.MessageID,
			"Session expired. Run /login to re-authenticate.")
		return
	}
	if errors.Is(err, runner.ErrNotLoggedIn) {
		b.replyTo(ctx, item.ChatID, item.ThreadID, item.MessageID,
			"Not authenticated. Run /login to sign in.")
		return
	}
	b.log.Error("streaming agent run failed",
		slog.String("chat_folder", item.Folder),
		slog.Any("err", err))
	b.replyTo(ctx, item.ChatID, item.ThreadID, item.MessageID,
		"Something went wrong. Check gopod logs for details.")
}

// drainAndSend is the fallback path: drain a streaming AgentStream
// into a buffer and send the result as a single formatted message.
// Used when the placeholder can't be sent.
func (b *Bot) drainAndSend(ctx context.Context, item queue.Item, stream *runner.AgentStream) {
	drainStream(stream)
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
	sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, text)
	b.recordReply(ctx, item, sent, text)
	b.maybeVoiceReply(ctx, item, text)
}

// drainStream discards a stream's remaining chunks and progress steps
// until Chunks closes, so the runner never blocks on a reader that has
// gone (Progress is unbuffered and closes before Chunks).
func drainStream(stream *runner.AgentStream) {
	progress := stream.Progress
	for chunks := stream.Chunks; chunks != nil; {
		select {
		case _, ok := <-chunks:
			if !ok {
				chunks = nil
			}
		case _, ok := <-progress:
			if !ok {
				progress = nil
			}
		}
	}
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
	text = stripCodeBlocks(text) // code is not read aloud
	if strings.TrimSpace(text) == "" {
		return
	}
	audioData, err := textToSpeech(ctx, apiKey, text)
	if err != nil {
		b.log.Warn("TTS failed",
			slog.Any("err", err))
		return
	}
	if err := b.sendVoiceReply(ctx, item.ChatID, item.ThreadID, item.MessageID, audioData); err != nil {
		b.log.Warn("send voice reply failed",
			slog.Any("err", err))
	}
}
