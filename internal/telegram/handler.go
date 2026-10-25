package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// defaultHandler is invoked by go-telegram/bot for every update that no
// registered handler matched. As of M6 it routes registered chats
// through the runner: persist the message, ensure the chat's container
// is up, ensure auth is configured, run claude -p, send the reply.
//
// Unregistered chats (other than the owner, which is auto-registered
// on first sight) get persisted and otherwise ignored. They can still
// use the public commands (/ping, /whoami, /register).
func (b *Bot) defaultHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update != nil && update.StoppedMessageGeneration != nil {
		// Stop button on a streaming draft (stopped_message_generation
		// is in allowedUpdates, bot.go).
		b.handleGenerationStopped(ctx, update)
		return
	}
	if update == nil || update.Message == nil {
		return
	}
	m := update.Message
	b.persistMessage(ctx, m)
	b.noteInboundThread(m)

	// Without a runner this stays at M1 echo-only behaviour.
	if b.runner == nil {
		return
	}

	text := strings.TrimSpace(messageText(m))
	// thread is the private-chat topic the message came from; every
	// answer to it below goes back into that topic.
	thread := inboundThreadID(m)

	// Handle voice messages (I3): transcribe via Whisper, use text as prompt.
	isVoice := false
	if m.Voice != nil {
		isVoice = true
		transcript, err := b.transcribeVoice(ctx, m.Voice)
		if err != nil {
			b.log.Error("voice transcription failed", slog.Any("err", err))
			b.replyTo(ctx, m.Chat.ID, thread, m.ID, "Failed to transcribe voice message.")
			return
		}
		if transcript != "" {
			text = transcript
			b.log.Debug("voice transcribed",
				slog.Int("duration", m.Voice.Duration),
				slog.Int("text_len", len(text)))
		}
	}

	// Handle photo messages (I2): download, save, build prompt.
	var filePath string
	if len(m.Photo) > 0 {
		rc, ok := b.resolveRegistered(ctx, m)
		if !ok {
			return
		}
		_, containerPath, err := b.downloadPhoto(ctx, rc.Folder, m.Photo)
		if err != nil {
			b.log.Error("download photo failed", slog.Any("err", err))
			b.replyTo(ctx, m.Chat.ID, thread, m.ID, "Failed to download photo.")
			return
		}
		filePath = containerPath
		if text == "" {
			text = "The user sent a photo. Describe what you see in it."
		} else {
			text = text + "\n\n[Photo attached at " + containerPath + "]"
		}
	}

	// Handle document messages: download, save, reference in prompt.
	if m.Document != nil && filePath == "" {
		rc, ok := b.resolveRegistered(ctx, m)
		if !ok {
			return
		}
		_, containerPath, err := b.downloadDocument(ctx, rc.Folder, m.Document)
		if err != nil {
			b.log.Error("download document failed", slog.Any("err", err))
			b.replyTo(ctx, m.Chat.ID, thread, m.ID, "Failed to download document.")
			return
		}
		filePath = containerPath
		if text == "" {
			text = "The user sent a file: " + containerPath + ". Read and analyze it."
		} else {
			text = text + "\n\n[File attached at " + containerPath + "]"
		}
	}

	if text == "" {
		return
	}

	// If this chat has an active /login session, the next non-empty
	// message is the OAuth code from the browser success page. Intercept
	// it and pipe it to claude's stdin instead of sending it to the
	// agent. Slash commands are NOT intercepted — the user might type
	// /help mid-login and that should still work.
	if b.logins.awaitingCode(m.Chat.ID, text) != nil {
		b.handleLoginCode(ctx, m.Chat.ID, text)
		return
	}

	// Slash commands are dispatched through the Router. /login is
	// handled separately (registered via go-telegram/bot's
	// MatchTypeCommand because it's a stateful interactive session).
	// Everything else that starts with "/" goes through here.
	if strings.HasPrefix(text, "/") {
		b.dispatchSlash(ctx, m, text)
		return
	}

	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		return
	}

	// Instant acknowledgement: the queued reaction (👀) so the user
	// knows the message was received, even before container spawns
	// (~15s). The turn moves it through its states (status.go).
	b.newStatusReactor(m.Chat.ID, m.ID).Set(stQueued)

	item := queue.Item{
		ChatID:    m.Chat.ID,
		MessageID: m.ID,
		ThreadID:  thread,
		Folder:    rc.Folder,
		IsOwner:   rc.IsOwner,
		Text:      text,
		FilePath:  filePath,
		IsVoice:   isVoice,
		Source:    "telegram",
		// Telegram message ids are unique only within a chat.
		SourceID: strconv.FormatInt(m.Chat.ID, 10) + ":" + strconv.Itoa(m.ID),
	}

	if b.queue != nil {
		// WithoutCancel: a shutdown mid-handler must still persist the
		// turn row so Recover picks it up, not drop the message.
		if id, dup := b.queue.Enqueue(context.WithoutCancel(ctx), item); !dup {
			item.ID = id
			// Waiting behind another conversation: show the position.
			b.startQueueWait(context.WithoutCancel(ctx), item)
		}
		return
	}
	// Sync fallback (no queue).
	b.runAgentSync(ctx, item)
}

// inboundThreadID returns the private-chat topic a message belongs to.
// Bot API 9.3+ stamps message_thread_id on messages inside a private-chat
// topic; the main chat carries none. Group and supergroup messages also
// carry message_thread_id (forum topics, reply chains), but gopod keeps
// one conversation per group, so those map to 0.
func inboundThreadID(m *models.Message) int {
	if m == nil || m.Chat.Type != models.ChatTypePrivate || m.MessageThreadID <= 0 {
		return 0
	}
	return m.MessageThreadID
}

// dispatchSlash parses a "/command arg1 arg2" message and dispatches
// it through the Router. If the Router is nil (pre-M3.5), falls back
// to "unknown command".
func (b *Bot) dispatchSlash(ctx context.Context, m *models.Message, text string) {
	thread := inboundThreadID(m)
	if b.router == nil {
		b.replyText(ctx, m.Chat.ID, thread, "unknown command")
		return
	}

	parts := strings.Fields(text)
	if len(parts) == 0 {
		return
	}
	// Strip leading "/" and optional "@botname" suffix.
	slash := parts[0]
	if i := strings.Index(slash, "@"); i > 0 {
		slash = slash[:i]
	}
	slash = strings.TrimPrefix(slash, "/")

	cmdName, ok := b.router.LookupBySlash(slash)
	if !ok {
		b.replyText(ctx, m.Chat.ID, thread, "unknown command: /"+slash)
		return
	}

	cmd := control.Command{
		Name: cmdName,
		Args: parts[1:],
		Caller: control.Caller{
			ChatID:   m.Chat.ID,
			ThreadID: thread,
			UserID:   userID(m.From),
			Username: senderDisplayName(m.From),
			Source:   control.SourceTelegram,
			IsOwner:  b.ownerChatID != 0 && m.Chat.ID == b.ownerChatID,
		},
	}

	// /compact runs an agent summary that can take a while: ✍ on the
	// command while it runs, then done or error like a turn.
	var status *statusReactor
	if cmdName == "compact" {
		status = b.newStatusReactor(m.Chat.ID, m.ID)
		status.Set(stCompacting)
	}
	resp, err := b.router.Dispatch(ctx, cmd)
	if err != nil {
		status.Set(stError)
		b.replyTo(ctx, m.Chat.ID, thread, m.ID, resp.Text)
		return
	}
	if resp.Code != 0 {
		status.Set(stError) // e.g. "Chat not registered.": not done
	} else {
		status.Set(stDone)
	}
	b.sendCommandResponse(ctx, m.Chat.ID, thread, m.ID, resp)
}

func userID(u *models.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// replyModes stores per-chat reply mode overrides. Default behavior:
// voice input → voice+text reply, text input → text reply.
// Override values: "voice", "text", "voice+text", "" (= auto).
var (
	replyModesMu sync.RWMutex
	replyModes   = make(map[int64]string)
)

// getReplyMode returns the effective reply mode for a chat.
// If no override is set, auto-detects: voice input → "voice+text",
// text input → "text".
func (b *Bot) getReplyMode(chatID int64, isVoice bool) string {
	replyModesMu.RLock()
	mode := replyModes[chatID]
	replyModesMu.RUnlock()

	if mode != "" {
		return mode
	}
	// Auto: mirror the input modality.
	if isVoice {
		return "voice+text"
	}
	return "text"
}

// SetReplyMode sets a per-chat reply mode override. Exported for
// the /voice Router command registered in main.go.
func SetReplyMode(chatID int64, mode string) {
	replyModesMu.Lock()
	if mode == "" || mode == "auto" {
		delete(replyModes, chatID)
	} else {
		replyModes[chatID] = mode
	}
	replyModesMu.Unlock()
}

// runAgentSync is the synchronous agent path. Used as the queue
// handler callback (and directly when Queue is nil).
func (b *Bot) runAgentSync(ctx context.Context, item queue.Item) {
	tier := runner.TierRegistered
	if item.IsOwner {
		tier = runner.TierOwner
	}

	// 👀 already set by defaultHandler before enqueue.
	status := b.turnStatus(item)
	status.Set(stThinking)
	stopTyping := b.startTyping(ctx, item.ChatID, item.ThreadID)
	defer stopTyping()

	prompt := item.Text
	if item.Resumed {
		prompt = resumePrompt(item.Text)
	}
	reply, err := b.runner.Run(ctx, item.Folder, item.ThreadID, tier, b.allowlist, prompt)
	if err != nil {
		b.replyError(ctx, item, err)
		return
	}

	status.Set(stDone)

	if reply == "" {
		reply = "(empty reply)"
	}
	b.deliverSyncReply(ctx, item, reply)
}

// deliverSyncReply sends the sync path's final reply: voice when the
// chat's reply mode asks for it, text otherwise. Voice-only mode sends
// no text, but the voice reply never reads code aloud (stripCodeBlocks),
// so a reply with code blocks also goes out as text: the code must
// reach the user in some form.
func (b *Bot) deliverSyncReply(ctx context.Context, item queue.Item, reply string) {
	b.maybeVoiceReply(ctx, item, reply)

	mode := b.getReplyMode(item.ChatID, item.IsVoice)
	if mode != "voice" || stripCodeBlocks(reply) != reply {
		sent, _ := b.sendFinal(ctx, item.ChatID, item.ThreadID, item.MessageID, reply, b.replyMarkup(item, reply))
		b.recordReply(ctx, item, sent, reply)
		b.noteReply(ctx, item, sent, reply)
	}
}

// NewAgentHandler returns a queue.Handler callback that the queue
// worker invokes for each coalesced batch of items. It runs the agent
// for the LAST item in the batch (the most recent user message) since
// each `claude -p` is a fresh session and earlier messages are already
// in the store for future reference.
//
// Call this after constructing the Bot and pass the result to
// queue.New. The circular dependency (Bot needs Queue, Queue needs
// Handler from Bot) is broken by constructing the Queue after the Bot.
func (b *Bot) NewAgentHandler() queue.Handler {
	return func(ctx context.Context, items []queue.Item) error {
		if len(items) == 0 {
			return nil
		}
		// Use the last item — the most recent user message — carrying
		// any resume state from earlier items in the batch.
		item := pickBatchItem(items)
		if item.Resumed && item.ReplyMsgID != 0 {
			// The crash hit between sending the reply and marking the
			// turn done. The user already has the answer.
			b.log.Info("resumed turn already replied, skipping",
				slog.Int64("turn", item.ID),
				slog.Int("reply_msg_id", item.ReplyMsgID))
			return nil
		}
		var runErr error
		if b.streamEnabled {
			runErr = b.runAgentStreaming(ctx, item)
		} else {
			b.runAgentSync(ctx, item)
		}
		if err := ctx.Err(); err != nil {
			// Shutdown cut the run short: report it so the queue marks
			// the turn interrupted rather than done.
			return err
		}
		if runErr != nil {
			// queue.ErrStopped: the queue marks the turn stopped.
			// errReplyUndelivered: the reply never reached the chat, so
			// the turn must not be marked done; the queue retries it.
			return runErr
		}
		// Both paths log and reply on error but always return nil so
		// the queue does not retry user-visible failures (auth errors,
		// agent crashes). Only infrastructure failures (Docker
		// unreachable, container won't start) should be retried.
		return nil
	}
}

// resolveRegistered looks up the chat's registered_chats row, auto-
// registering the owner chat on first sight. Returns (rc, true) when
// the chat is registered (existing or freshly auto-registered), or
// (zero, false) for an unregistered non-owner.
func (b *Bot) resolveRegistered(ctx context.Context, m *models.Message) (store.RegisteredChat, bool) {
	chatJID := buildChatJID(m.Chat.ID)
	rc, err := b.store.GetRegistered(ctx, chatJID)
	if err == nil {
		return rc, true
	}
	if !errors.Is(err, store.ErrChatNotRegistered) {
		b.log.Error("GetRegistered failed",
			slog.String("chat_jid", chatJID),
			slog.Any("err", err))
		return store.RegisteredChat{}, false
	}

	// Auto-register the owner chat the first time we see it. Folder
	// is fixed to "owner" — the owner has exactly one chat folder by
	// design (D006).
	if b.ownerChatID != 0 && m.Chat.ID == b.ownerChatID {
		newRc := store.RegisteredChat{
			JID:     chatJID,
			Name:    chatDisplayName(&m.Chat),
			Folder:  "owner",
			IsOwner: true,
			AddedAt: time.Now().UnixMilli(),
		}
		if err := b.store.RegisterChat(ctx, newRc); err != nil {
			b.log.Error("auto-register owner failed",
				slog.String("chat_jid", chatJID),
				slog.Any("err", err))
			return store.RegisteredChat{}, false
		}
		b.log.Info("auto-registered owner chat",
			slog.String("chat_jid", chatJID),
			slog.String("folder", newRc.Folder))
		return newRc, true
	}

	return store.RegisteredChat{}, false
}

// startTyping fires a "typing..." chat action immediately and then
// refreshes it every 4 seconds in a goroutine. Returns a stop func
// the caller defers — that cancels the refresh loop. thread is the
// private-chat topic (0 = main chat) the action shows in.
func (b *Bot) startTyping(ctx context.Context, chatID int64, thread int) func() {
	send := func() {
		if b.threadGone(chatID, thread) {
			return // no typing into a topic that no longer exists
		}
		_, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{
			ChatID:          chatID,
			MessageThreadID: thread,
			Action:          models.ChatActionTyping,
		})
		if err != nil && !b.checkThreadGone(chatID, thread, err) {
			// Best-effort; don't spam logs on every refresh failure.
			b.log.Debug("SendChatAction failed",
				slog.Int64("chat_id", chatID),
				slog.Any("err", err))
		}
	}
	send()

	stopCh := make(chan struct{})
	go func() {
		t := time.NewTicker(4 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-stopCh:
				return
			case <-ctx.Done():
				return
			case <-t.C:
				send()
			}
		}
	}()
	return func() { close(stopCh) }
}

// Outbound sends take thread, the private-chat topic the conversation
// lives in (message_thread_id), next to chatID. 0 is the main chat:
// MessageThreadID is omitempty in go-telegram/bot, so a thread-0 send
// is the same request as before topics existed.

// replyTo sends a message as a reply to a specific message, with
// markdown → HTML conversion and smart chunking. Falls back to plain
// text if HTML send fails.
func (b *Bot) replyTo(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string) int {
	return b.sendFormatted(ctx, chatID, thread, replyToMsgID, text)
}

// replyText sends a message without reply threading. Used by
// commands and system messages.
func (b *Bot) replyText(ctx context.Context, chatID int64, thread int, text string) {
	_ = b.sendFormatted(ctx, chatID, thread, 0, text)
}

func (b *Bot) sendFormatted(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string) int {
	id, _ := b.sendFormattedErr(ctx, chatID, thread, replyToMsgID, text)
	return id
}

// sendFormattedErr is sendFormatted reporting the error that stopped
// it. The id is the last message delivered, 0 when none was.
//
// When thread is a topic that no longer exists (threadgone.go), known
// before or learned from this send, the whole text goes to the main
// chat instead, prefixed with threadGoneNote and replying to nothing
// (the original message went with the topic). Chunks already delivered
// into the topic went with it too, so the full text is resent.
func (b *Bot) sendFormattedErr(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string) (int, error) {
	return b.sendRendered(ctx, chatID, thread, replyToMsgID, text, markdownToTelegramHTML, nil)
}

// sendRendered is sendFormattedErr with the markdown-to-HTML renderer
// as a parameter: the final reply renders with finalReplyHTML. markup
// (nil for none) goes on the last chunk only: the final reply's action
// row sits under the end of the reply. The reply redirected to main from
// a gone topic gets none.
func (b *Bot) sendRendered(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string, render func(string) string, markup models.ReplyMarkup) (int, error) {
	return b.sendRenderedOpts(ctx, chatID, thread, replyToMsgID, text, render, markup, false)
}

// sendRenderedOpts is sendRendered; keepMarkup keeps the buttons on the
// topic-gone fallback in main. Command replies (pickers and pages)
// need theirs there: their tokens are bound to the chat, not the
// topic, and a question without its buttons cannot be answered.
func (b *Bot) sendRenderedOpts(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string, render func(string) string, markup models.ReplyMarkup, keepMarkup bool) (int, error) {
	if !b.threadGone(chatID, thread) {
		id, err := b.sendChunks(ctx, chatID, thread, replyToMsgID, text, render, markup)
		if !b.checkThreadGone(chatID, thread, err) {
			return id, err
		}
	}
	// The topic-gone fallback in main carries no reply action row
	// (replyrow.go): that row is the topic conversation's.
	if !keepMarkup {
		markup = nil
	}
	return b.sendChunks(ctx, chatID, 0, 0, threadGoneNote+text, render, markup)
}

// sendChunks sends text as HTML chunks into thread, falling back to
// plain text when Telegram rejects the markup. render turns the
// markdown into HTML, which chunkHTML splits without breaking tags (an
// element cut by a chunk boundary is closed and reopened). A 429 on the HTML
// attempt is returned as is: the plain-text fallback is for markup
// Telegram rejects and would only hit the same rate limit. So is a
// thread-gone error, which the plain text would hit as well.
func (b *Bot) sendChunks(ctx context.Context, chatID int64, thread int, replyToMsgID int, text string, render func(string) string, markup models.ReplyMarkup) (int, error) {
	const maxLen = 4000
	last := 0
	htmlText := render(text)
	chunks := chunkHTML(htmlText, maxLen)

	for i, c := range chunks {
		params := &bot.SendMessageParams{
			ChatID:          chatID,
			MessageThreadID: thread,
			Text:            c,
			ParseMode:       models.ParseModeHTML,
		}
		if i == len(chunks)-1 {
			params.ReplyMarkup = markup
		}
		// Reply threading: only the first chunk replies to the original message.
		if i == 0 && replyToMsgID > 0 {
			params.ReplyParameters = &models.ReplyParameters{
				MessageID:                replyToMsgID,
				AllowSendingWithoutReply: true,
			}
		}

		sent, err := b.api.SendMessage(ctx, params)
		if err == nil && sent != nil {
			last = sent.ID
		}
		fatal := func(err error) bool {
			return errors.As(err, new(*bot.TooManyRequestsError)) || (thread != 0 && isThreadGone(err))
		}
		if err != nil && params.ReplyMarkup != nil && !fatal(err) {
			// The keyboard may be what Telegram refused: send this same
			// HTML chunk again without it. Buttons are a convenience;
			// the formatted text is not, and earlier chunks are out.
			b.log.Warn("reply keyboard refused, sending the chunk without it",
				slog.Int64("chat_id", chatID), slog.Any("err", err))
			params.ReplyMarkup = nil
			sent, err = b.api.SendMessage(ctx, params)
			if err == nil {
				if sent != nil {
					last = sent.ID
				}
				continue
			}
		}
		if err != nil {
			if fatal(err) {
				return last, fmt.Errorf("sendMessage: %w", err)
			}
			// Fallback: send as plain text, only what has not gone out:
			// the whole markdown when no chunk was delivered, else the
			// remaining HTML chunks with their tags stripped.
			b.log.Debug("HTML send failed, falling back to plain text",
				slog.Int64("chat_id", chatID),
				slog.Any("err", err))
			plainChunks := chunkString(text, maxLen)
			if i > 0 {
				plainChunks = nil
				for _, rest := range chunks[i:] {
					plainChunks = append(plainChunks, htmlToPlain(rest))
				}
			}
			for j, pc := range plainChunks {
				pp := &bot.SendMessageParams{
					ChatID:          chatID,
					MessageThreadID: thread,
					Text:            pc,
				}
				if j == len(plainChunks)-1 {
					pp.ReplyMarkup = markup
				}
				sent, err := b.api.SendMessage(ctx, pp)
				if err != nil && pp.ReplyMarkup != nil && !errors.As(err, new(*bot.TooManyRequestsError)) {
					// A refused keyboard never costs the reply: the
					// buttons are a convenience, the text is not.
					b.log.Warn("reply keyboard refused, sending without it",
						slog.Int64("chat_id", chatID), slog.Any("err", err))
					pp.ReplyMarkup = nil
					sent, err = b.api.SendMessage(ctx, pp)
				}
				if err != nil {
					b.log.Error("SendMessage failed",
						slog.Int64("chat_id", chatID),
						slog.Any("err", err))
					return last, fmt.Errorf("sendMessage: %w", err)
				}
				if sent != nil {
					last = sent.ID
				}
			}
			return last, nil
		}
	}
	return last, nil
}

// chunkString splits s into pieces of at most n bytes. Prefers
// splitting on double-newline (paragraph boundary) > single newline >
// last space. Never splits inside <pre>...</pre> blocks if possible.
func chunkString(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		split := n
		// Try double-newline (paragraph) in the second half.
		if i := strings.LastIndex(s[:n], "\n\n"); i > n/3 {
			split = i + 2
		} else if i := strings.LastIndex(s[:n], "\n"); i > n/3 {
			split = i + 1
		} else if i := strings.LastIndex(s[:n], " "); i > n/3 {
			split = i + 1
		}
		out = append(out, s[:split])
		s = s[split:]
	}
	if len(s) > 0 {
		out = append(out, s)
	}
	return out
}

// pingHandler answers /ping with "pong" and persists the inbound /ping
// message itself (registered handlers do not fall through to the default
// handler in go-telegram/bot, so storage has to happen here too).
func (b *Bot) pingHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	b.persistMessage(ctx, update.Message)
	b.replyText(ctx, update.Message.Chat.ID, inboundThreadID(update.Message), "pong")
}

// persistMessage upserts the chat row and stores the message body.
// Errors are logged but not propagated — telegram delivery is best-effort
// and the long-poll loop must keep running through transient DB hiccups.
func (b *Bot) persistMessage(ctx context.Context, m *models.Message) {
	chatJID := buildChatJID(m.Chat.ID)
	chatName := chatDisplayName(&m.Chat)
	tsMS := int64(m.Date) * 1000 // Telegram dates are unix seconds

	if err := b.store.UpsertChat(ctx, store.ChatRecord{
		JID:             chatJID,
		Name:            chatName,
		LastMessageTime: tsMS,
		IsGroup:         isGroupChat(m.Chat.Type),
	}); err != nil {
		b.log.Error("telegram: UpsertChat failed",
			slog.String("chat_jid", chatJID),
			slog.Any("err", err))
		// Continue: we still want to try to save the message itself.
	}

	msg := store.Message{
		ChatJID:      chatJID,
		TGMessageID:  int64(m.ID),
		Sender:       senderID(m.From),
		SenderName:   senderDisplayName(m.From),
		Content:      messageText(m),
		Timestamp:    tsMS,
		IsBotMessage: m.From != nil && m.From.IsBot,
	}
	if r := m.ReplyToMessage; r != nil {
		msg.ReplyToTGMessageID = int64(r.ID)
		msg.ReplyToContent = messageText(r)
		msg.ReplyToSenderName = senderDisplayName(r.From)
	}

	id, err := b.store.SaveMessage(ctx, msg)
	if err != nil {
		b.log.Error("telegram: SaveMessage failed",
			slog.String("chat_jid", chatJID),
			slog.Int64("tg_msg_id", msg.TGMessageID),
			slog.Any("err", err))
		return
	}
	b.log.Debug("telegram: stored message",
		slog.String("chat_jid", chatJID),
		slog.Int64("tg_msg_id", msg.TGMessageID),
		slog.Int64("row_id", id),
		slog.String("sender", msg.SenderName),
		slog.Int("len", len(msg.Content)),
	)
}

// buildChatJID returns the gopod canonical "tg:<chat_id>" form. The
// telegram chat ID is signed (negative for groups) and we keep that as-is
// in the string for symmetry with NanoClaw.
func buildChatJID(chatID int64) string {
	return "tg:" + strconv.FormatInt(chatID, 10)
}

// isGroupChat reports whether a Telegram Chat.Type is one of the
// many-user kinds.
func isGroupChat(t models.ChatType) bool {
	switch t {
	case models.ChatTypeGroup, models.ChatTypeSupergroup, models.ChatTypeChannel:
		return true
	default:
		return false
	}
}

// chatDisplayName picks the human label for a chat: title for groups,
// constructed name for private chats.
func chatDisplayName(c *models.Chat) string {
	if c == nil {
		return ""
	}
	if c.Title != "" {
		return c.Title
	}
	parts := []string{c.FirstName, c.LastName}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name != "" {
		return name
	}
	if c.Username != "" {
		return "@" + c.Username
	}
	return ""
}

// senderID returns the Telegram numeric user ID as a string, or "" if
// the From field is missing (channel posts have no From).
func senderID(u *models.User) string {
	if u == nil {
		return ""
	}
	return strconv.FormatInt(u.ID, 10)
}

// senderDisplayName picks a friendly label for a Telegram user.
func senderDisplayName(u *models.User) string {
	if u == nil {
		return ""
	}
	parts := []string{u.FirstName, u.LastName}
	name := strings.TrimSpace(strings.Join(parts, " "))
	if name != "" {
		return name
	}
	if u.Username != "" {
		return "@" + u.Username
	}
	return strconv.FormatInt(u.ID, 10)
}

// messageText returns the textual content of a message: Text for plain
// messages, Caption for media messages with a caption, otherwise an
// empty string. M1 only stores text — media handling lands in I2/I3/I4.
func messageText(m *models.Message) string {
	if m == nil {
		return ""
	}
	if m.Text != "" {
		return m.Text
	}
	return m.Caption
}
