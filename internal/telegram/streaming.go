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

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// sendPlaceholder sends a "▍" (blinking cursor) message and returns
// its message ID. The caller uses this ID for subsequent editMessage
// calls to stream partial output. Returns 0 if the send fails.
//
// thread is the private-chat topic (0 = main chat). replyToMsgID is
// the user's original message ID for reply threading; pass 0 for no
// threading. markup is the placeholder's keyboard (turnKeyboard: the
// Stop button of a stoppable turn), nil for none.
func (b *Bot) sendPlaceholder(ctx context.Context, chatID int64, thread int, replyToMsgID int, markup models.ReplyMarkup) int {
	return b.sendPlaceholderText(ctx, chatID, thread, replyToMsgID, "\u2758", markup) // ▍ cursor
}

// sendPlaceholderText is sendPlaceholder with a given opening text.
func (b *Bot) sendPlaceholderText(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string, markup models.ReplyMarkup) int {
	if b.threadGone(chatID, thread) {
		return 0 // the caller falls back to one final message, sent to main
	}
	params := &bot.SendMessageParams{
		ChatID:          chatID,
		MessageThreadID: thread,
		Text:            text,
		ReplyMarkup:     markup,
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

// editMessage edits a previously sent message with plain text content
// and removes its inline keyboard (a placeholder's Stop button): every
// caller ends the placeholder's streaming life. Errors are logged and
// returned; mid-stream callers ignore them — a failed edit means the
// user sees a slightly stale partial; the next edit or final message
// fixes it.
func (b *Bot) editMessage(ctx context.Context, chatID int64, msgID int, text string) error {
	return b.editMessageKB(ctx, chatID, msgID, text, noKeyboard())
}

// editMessageKB is editMessage with the keyboard to show after the edit
// (nil: the request carries none). A streaming placeholder passes its
// turnKeyboard on every edit, so the text and the Stop button change in
// one call.
func (b *Bot) editMessageKB(ctx context.Context, chatID int64, msgID int, text string, markup models.ReplyMarkup) error {
	b.controlMu.Lock()
	defer b.controlMu.Unlock()
	return b.editMessageKBLocked(ctx, chatID, msgID, text, markup)
}
func (b *Bot) editMessageKBLocked(ctx context.Context, chatID int64, msgID int, text string, markup models.ReplyMarkup) error {
	if !b.controlEditAllowed(ctx, chatID, msgID, markup) {
		return nil
	}
	text = b.safeControlText(ctx, chatID, msgID, text, markup)
	b.checkpointControl(ctx, chatID, msgID, text, false, markup)
	if text == "" {
		text = "\u2758" // keep cursor visible if empty
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   msgID,
		Text:        text,
		ReplyMarkup: markup,
	})
	if err != nil {
		b.log.Debug("editMessage failed",
			slog.Int64("chat_id", chatID),
			slog.Int("msg_id", msgID),
			slog.Any("err", err))
		return fmt.Errorf("editMessageText: %w", err)
	}
	if b.store != nil {
		if err := b.store.AcknowledgeControlEdit(ctx, chatID, msgID); err != nil {
			b.log.Warn("control edit acknowledge failed", slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
		}
	}
	return nil
}

// editMessageHTML edits a previously sent message with HTML-formatted
// content and removes its inline keyboard: it is the final edit of a
// placeholder. Falls back to plain text if the HTML edit fails (e.g.
// due to malformed tags). Returns the error of the last attempt.
func (b *Bot) editMessageHTML(ctx context.Context, chatID int64, msgID int, html string) error {
	return b.editMessageHTMLKB(ctx, chatID, msgID, html, noKeyboard())
}

// editMessageHTMLKB is editMessageHTML with the keyboard to show after
// the edit: the final edit of a placeholder that becomes the reply
// swaps its Stop button for the reply action row in the same call.
// When the plain-text fallback fails too while markup has buttons, the
// edit is tried once more without them: a refused keyboard never costs
// the reply.
func (b *Bot) editMessageHTMLKB(ctx context.Context, chatID int64, msgID int, html string, markup models.ReplyMarkup) error {
	b.controlMu.Lock()
	defer b.controlMu.Unlock()
	return b.editMessageHTMLKBLocked(ctx, chatID, msgID, html, markup)
}
func (b *Bot) editMessageHTMLKBLocked(ctx context.Context, chatID int64, msgID int, html string, markup models.ReplyMarkup) error {
	if !b.controlEditAllowed(ctx, chatID, msgID, markup) {
		return nil
	}
	html = b.safeControlText(ctx, chatID, msgID, html, markup)
	b.checkpointControl(ctx, chatID, msgID, html, true, markup)
	if html == "" {
		return nil
	}
	_, err := b.api.EditMessageText(ctx, &bot.EditMessageTextParams{
		ChatID:      chatID,
		MessageID:   msgID,
		Text:        html,
		ParseMode:   models.ParseModeHTML,
		ReplyMarkup: markup,
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
		perr := b.editMessageKBLocked(ctx, chatID, msgID, htmlToPlain(html), markup)
		if perr != nil && hasButtons(markup) && !isNotModified(perr) {
			if _, limited := retryAfter(perr); !limited {
				return b.editMessageHTMLKBLocked(ctx, chatID, msgID, html, noKeyboard())
			}
		}
		return perr
	}
	if b.store != nil {
		if err := b.store.AcknowledgeControlEdit(ctx, chatID, msgID); err != nil {
			b.log.Warn("control edit acknowledge failed", slog.Int64("chat_id", chatID), slog.Int("msg_id", msgID), slog.Any("err", err))
		}
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
	defer b.registerInflight(item.ID, item.ChatID, item.ThreadID, cancelRun)()
	// No queue-position draft may land in this thread from now on.
	defer b.markThreadBusy(item)()
	b.stopQueueWaits(item.ChatID, item.ThreadID)
	b.preparePrivateStop(ctx, item)

	waitCtx := runner.WithWaitNotice(runCtx, b.waitNotice(ctx, item))
	stream, err := b.runner.RunStream(waitCtx, item.Folder, item.ThreadID, tier, b.allowlist, prompt)
	// The wait is over: no wait draft refresh may land after this point.
	b.stopWaitDraftRefresh(item)
	if err != nil {
		// Stopped (or shut down) before the agent started: no fallback.
		if handled, err := b.streamNotStarted(ctx, runCtx, item); handled {
			return err
		}
		// Fallback: if streaming setup fails, try sync path.
		b.log.Warn("RunStream failed, falling back to sync",
			slog.String("folder", item.Folder),
			slog.Any("err", err))
		return b.runAgentSyncContext(ctx, runCtx, item)
	}
	b.turnStatus(item).Set(stThinking) // the agent runs
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
	status := b.turnStatus(item)
	// Silent mode (D055): answer text never reaches the draft or the
	// placeholder; the view stays on progress until the final message.
	silent := b.silentMode(ctx, item)
	stopOpts := draftOpts{CanStop: stop != nil, KeepOnStop: stop != nil}
	// kb is the edit path's Stop button (nil when not stoppable): on
	// the placeholder and on every mid-stream edit of it.
	kb := b.turnKeyboard(item.ID)
	// useDraft makes id the turn's current draft, the one a stop names.
	useDraft := func(id int) int {
		b.bindDraft(item.ID, item.ChatID, id)
		return id
	}

	// startPlaceholder switches the turn to the edit path. Returns false
	// when no placeholder could be sent.
	placeholder := 0
	startPlaceholder := func() bool {
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.ThreadID, item.MessageID, kb)
		if placeholder == 0 {
			return false
		}
		b.recordPlaceholder(ctx, item, placeholder)
		return true
	}

	draftID := 0 // non-zero while the turn streams through drafts
	var lastDraft time.Time
	var draftHold time.Time // no draft updates before this (429 retry_after)
	if placeholder = b.takeWaitPlaceholder(ctx, item); placeholder != 0 {
		// Sent before startup or while the turn waited for the agent gate.
	} else if placeholder = b.reusePlaceholder(ctx, item); placeholder != 0 {
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
	stepCount := 0     // every step of the turn, for the header
	answering := false // answer text arrived since the latest step
	// Until answer text streams, the status header (elapsed time, step
	// count) sits above the steps; a turn with neither steps nor text
	// shows it only after headerQuiet.
	started := b.statusNow()
	view := func() string {
		if answering {
			return accumulated.String()
		}
		elapsed := b.statusNow().Sub(started)
		if len(steps) > 0 {
			return statusHeader(elapsed, stepCount, 0) + "\n" + progressView(steps)
		}
		if elapsed >= headerQuiet {
			return statusHeader(elapsed, 0, 0) + "\n"
		}
		return accumulated.String()
	}
	// Edit path: what the placeholder shows, when it was last edited,
	// and no edits before editHold (a 429's retry_after).
	var lastEditText string
	var lastEdit, editHold time.Time
	var lastDraftText string
	var lastDraftAt time.Time // status clock of the last draft sent

	// addChunk applies one answer chunk. Returns false when the turn
	// had to be finished by drainAndSend.
	addChunk := func(chunk string) bool {
		status.Set(stThinking)
		full.WriteString(chunk)
		if silent {
			return true // kept for a stop only; the view stays on progress
		}
		accumulated.WriteString(chunk)
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
		placeholder = b.sendPlaceholder(ctx, item.ChatID, item.ThreadID, 0, kb)
		if placeholder == 0 {
			b.drainAndSend(ctx, item, stream)
			return false
		}
		b.recordPlaceholder(ctx, item, placeholder)
		accumulated.Reset()
		dirty = false
		return true
	}

	// stopped ends a stopped turn: the draft path sends the text so far
	// as a new message; the edit path makes the placeholder the reply.
	stopped := func() error {
		if placeholder == 0 {
			return b.finishStopped(ctx, item, stream, full.String())
		}
		return b.finishStoppedEdit(ctx, item, stream, placeholder, full.String(), overflow, accumulated.String())
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
			status.Set(stTool)
			stepCount++
			line := progressLine(ev)
			b.logStep(item, line) // for the Steps button (replyrow.go)
			steps = append(steps, line)
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
				// The header's clock changes the text by itself. Drafts
				// share the per-peer budget of the typing action (20 per
				// 5 s, 40 per 30 s), so a change of the clock alone goes
				// out at most every headerDraftEvery; steps and answer
				// text (dirty) still go on the tick.
				text := view() + "❘" // ▍ cursor, never empty
				clockOnly := !dirty && text != lastDraftText
				if clockOnly && b.statusNow().Sub(lastDraftAt) < headerDraftEvery {
					continue
				}
				if !dirty && text == lastDraftText && time.Since(lastDraft) < draftRefresh {
					continue
				}
				if time.Now().Before(draftHold) {
					continue // rate limited: wait out retry_after
				}
				err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, draftID, text, stopOpts)
				if err == nil {
					lastDraft = time.Now()
					lastDraftAt = b.statusNow()
					lastDraftText = text
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
			// Edit path: a changed view goes out on the tick; a change
			// of the header's clock alone at most every headerEditEvery.
			// Every edit carries kb, so the Stop button stays.
			now := b.statusNow() // before view: the text is never older
			v := view()
			if v == "" || time.Now().Before(editHold) {
				continue
			}
			text := v + "❘"
			headerOnly := !dirty && !answering && now.Sub(lastEdit) >= headerEditEvery
			if text == lastEditText || !dirty && !headerOnly {
				dirty = false
				continue
			}
			err := b.editMessageKB(ctx, item.ChatID, placeholder, text, kb)
			if wait, ok := retryAfter(err); ok {
				editHold = time.Now().Add(wait)
				continue // dirty stays: retried after the wait
			}
			lastEditText, lastEdit = text, now
			dirty = false

		case <-stop:
			return stopped()

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
		return stopped()
	default:
	}
	select {
	case res = <-stream.Result:
	case <-stop:
		return stopped()
	}
	select {
	case <-stop:
		return stopped()
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
		sent, err := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, finalText, b.replyMarkup(item, finalText))
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
		b.noteReply(ctx, item, sent, finalText)
	case !overflow:
		// Everything fits in the placeholder. Oversized code goes out as
		// documents first; the reply in main keeps their notes.
		shown := b.attachLongCode(ctx, item.ChatID, item.ThreadID, finalText)
		err := b.editMessageHTMLKB(ctx, item.ChatID, placeholder, finalReplyHTML(shown), b.replyMarkup(item, finalText))
		if b.finalEditLost(item, err) {
			if err := b.replyInMain(ctx, item, shown); err != nil {
				return err
			}
			break
		}
		b.recordReply(ctx, item, placeholder, finalText)
		b.noteReply(ctx, item, placeholder, finalText)
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
		} else if kb != nil {
			b.clearKeyboard(ctx, item.ChatID, placeholder)
		}
		sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, finalText, b.replyMarkup(item, finalText))
		b.recordReply(ctx, item, sent, finalText)
		b.noteReply(ctx, item, sent, finalText)
	}

	status.Set(stDone)

	// Voice reply (must wait for full text).
	b.maybeVoiceReply(ctx, item, finalText)
	return nil
}

// silentMode reports whether the turn's chat has silent mode on
// (control.SilentStateKey, D055). A lookup failure logs and streams
// normally.
func (b *Bot) silentMode(ctx context.Context, item queue.Item) bool {
	if b.store == nil {
		return false
	}
	v, err := b.store.GetState(ctx, control.SilentStateKey+item.Folder)
	if err != nil {
		b.log.Warn("silent mode lookup failed",
			slog.Int64("chat_id", item.ChatID), slog.String("folder", item.Folder), slog.Any("err", err))
		return false
	}
	return v == "1"
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
	sent, err := b.sendFinal(ctx, item.ChatID, 0, 0, threadGoneNote+text, nil)
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
	// The topic-gone fallback has no row and is main's newest reply.
	b.newestReply(ctx, item.ChatID, 0, sent, false)
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
// markup (replyMarkup, nil for none) goes on the last text message
// only, never on a document.
func (b *Bot) sendFinal(ctx context.Context, chatID int64, thread int, replyTo int, text string, markup models.ReplyMarkup) (int, error) {
	text = b.attachLongCode(ctx, chatID, thread, text)
	var waited time.Duration
	for attempt := 1; ; attempt++ {
		id, err := b.sendRendered(ctx, chatID, thread, replyTo, text, finalReplyHTML, markup)
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
	sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, text, nil)
	b.recordReply(ctx, item, sent, text)
	b.noteBareReply(ctx, item, sent) // a stopped reply has no row
	b.turnStatus(item).Set(stStopped)
	return queue.ErrStopped
}

// finishStoppedEdit ends a turn stopped on the edit path. When the reply
// fit one placeholder, that placeholder becomes the reply: final text
// (the text so far), keyboard removed, recorded as the turn's reply.
// After an overflow it is frozen like a finished overflow turn's last
// placeholder and the whole stopped text goes out as a new message.
// Like finishStopped it drains the agent's remaining output in the
// background and returns queue.ErrStopped.
func (b *Bot) finishStoppedEdit(ctx context.Context, item queue.Item, stream *runner.AgentStream, placeholder int, partial string, overflow bool, tail string) error {
	go func() {
		drainStream(stream)
		<-stream.Result
	}()

	text := strings.TrimSpace(partial)
	if text == "" {
		text = stoppedEmpty
	}
	if !overflow {
		shown := b.attachLongCode(ctx, item.ChatID, item.ThreadID, text)
		err := b.editMessageHTML(ctx, item.ChatID, placeholder, finalReplyHTML(shown))
		if b.finalEditLost(item, err) {
			if err := b.replyInMain(ctx, item, shown); err != nil {
				return err
			}
		} else {
			b.recordReply(ctx, item, placeholder, text)
			b.noteBareReply(ctx, item, placeholder)
		}
	} else {
		if tail != "" {
			b.editMessage(ctx, item.ChatID, placeholder, tail)
		} else {
			b.clearKeyboard(ctx, item.ChatID, placeholder)
		}
		sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, text, nil)
		b.recordReply(ctx, item, sent, text)
		b.noteBareReply(ctx, item, sent)
	}
	b.turnStatus(item).Set(stStopped)
	return queue.ErrStopped
}

// handleStreamError handles errors from a streaming agent run: a
// humane reply from the error table (errors.go), as on the sync path.
func (b *Bot) handleStreamError(ctx context.Context, item queue.Item, err error) {
	b.replyError(ctx, item, err)
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

	b.turnStatus(item).Set(stDone)
	sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, text, b.replyMarkup(item, text))
	b.recordReply(ctx, item, sent, text)
	b.noteReply(ctx, item, sent, text)
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
	// Bind personal credentials to the configured private destination, never
	// mutable registration metadata such as IsOwner.
	spoken, ok := b.voiceCandidate(item, text)
	if !ok {
		return
	}
	if b.prepared != nil && b.prepared.Speech != nil {
		b.preparedVoiceReply(ctx, item, spoken)
		return
	}
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return
	}
	b.chatAction(ctx, item.ChatID, item.ThreadID, models.ChatActionRecordVoice)
	synth := b.tts
	if synth == nil {
		synth = textToSpeech
	}
	audioData, err := synth(ctx, apiKey, spoken)
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
