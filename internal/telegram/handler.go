package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/spaceinvaderz/picoclaw/internal/control"
	"github.com/spaceinvaderz/picoclaw/internal/queue"
	"github.com/spaceinvaderz/picoclaw/internal/runner"
	"github.com/spaceinvaderz/picoclaw/internal/store"
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
	if update == nil || update.Message == nil {
		return
	}
	m := update.Message
	b.persistMessage(ctx, m)

	// Without a runner this stays at M1 echo-only behaviour.
	if b.runner == nil {
		return
	}

	text := strings.TrimSpace(messageText(m))

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
			b.replyTo(ctx, m.Chat.ID, m.ID, "Failed to download photo.")
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
			b.replyTo(ctx, m.Chat.ID, m.ID, "Failed to download document.")
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
	if !strings.HasPrefix(text, "/") && b.logins.get(m.Chat.ID) != nil {
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

	item := queue.Item{
		ChatID:    m.Chat.ID,
		MessageID: m.ID,
		Folder:    rc.Folder,
		IsOwner:   rc.IsOwner,
		Text:      text,
		FilePath:  filePath,
	}

	// If queue is wired (M3+), enqueue and return immediately.
	// If not (pre-M3 fallback), run synchronously.
	if b.queue != nil {
		b.queue.Enqueue(ctx, item)
		return
	}
	// Sync fallback (no queue).
	b.runAgentSync(ctx, item)
}

// dispatchSlash parses a "/command arg1 arg2" message and dispatches
// it through the Router. If the Router is nil (pre-M3.5), falls back
// to "unknown command".
func (b *Bot) dispatchSlash(ctx context.Context, m *models.Message, text string) {
	if b.router == nil {
		b.replyText(ctx, m.Chat.ID, "unknown command")
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
		b.replyText(ctx, m.Chat.ID, "unknown command: /"+slash)
		return
	}

	cmd := control.Command{
		Name: cmdName,
		Args: parts[1:],
		Caller: control.Caller{
			ChatID:   m.Chat.ID,
			UserID:   userID(m.From),
			Username: senderDisplayName(m.From),
			Source:   control.SourceTelegram,
			IsOwner:  b.ownerChatID != 0 && m.Chat.ID == b.ownerChatID,
		},
	}

	resp, err := b.router.Dispatch(ctx, cmd)
	if err != nil {
		b.replyTo(ctx, m.Chat.ID, m.ID, resp.Text)
		return
	}
	if resp.Text != "" {
		b.replyTo(ctx, m.Chat.ID, m.ID, resp.Text)
	}
}

func userID(u *models.User) int64 {
	if u == nil {
		return 0
	}
	return u.ID
}

// runAgentSync is the synchronous agent path. Used as the queue
// handler callback (and directly when Queue is nil).
func (b *Bot) runAgentSync(ctx context.Context, item queue.Item) {
	tier := runner.TierRegistered
	if item.IsOwner {
		tier = runner.TierOwner
	}

	// 👀 reaction = "processing"
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiThinking)
	}

	stopTyping := b.startTyping(ctx, item.ChatID)
	defer stopTyping()

	reply, err := b.runner.Run(ctx, item.Folder, tier, b.allowlist, item.Text)
	if err != nil {
		// ❌ reaction on error
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
		b.log.Error("runner.Run failed",
			slog.String("chat_folder", item.Folder),
			slog.Any("err", err))
		b.replyTo(ctx, item.ChatID, item.MessageID,
			"Something went wrong. Check picoclaw logs for details.")
		return
	}

	// ✅ reaction on success
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}

	if reply == "" {
		reply = "(empty reply)"
	}
	b.replyTo(ctx, item.ChatID, item.MessageID, reply)
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
		// Use the last item — the most recent user message.
		item := items[len(items)-1]
		b.runAgentSync(ctx, item)
		// runAgentSync logs and replies on error but always returns
		// nil so the queue does not retry user-visible failures
		// (auth errors, agent crashes). Only infrastructure failures
		// (Docker unreachable, container won't start) should be retried.
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
// the caller defers — that cancels the refresh loop.
func (b *Bot) startTyping(ctx context.Context, chatID int64) func() {
	send := func() {
		_, err := b.api.SendChatAction(ctx, &bot.SendChatActionParams{
			ChatID: chatID,
			Action: models.ChatActionTyping,
		})
		if err != nil {
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

// replyTo sends a message as a reply to a specific message, with
// markdown → HTML conversion and smart chunking. Falls back to plain
// text if HTML send fails.
func (b *Bot) replyTo(ctx context.Context, chatID int64, replyToMsgID int, text string) {
	b.sendFormatted(ctx, chatID, replyToMsgID, text)
}

// replyText sends a message without reply threading. Used by
// commands and system messages.
func (b *Bot) replyText(ctx context.Context, chatID int64, text string) {
	b.sendFormatted(ctx, chatID, 0, text)
}

func (b *Bot) sendFormatted(ctx context.Context, chatID int64, replyToMsgID int, text string) {
	const maxLen = 4000
	htmlText := markdownToTelegramHTML(text)
	chunks := chunkString(htmlText, maxLen)

	for i, c := range chunks {
		params := &bot.SendMessageParams{
			ChatID:    chatID,
			Text:      c,
			ParseMode: models.ParseModeHTML,
		}
		// Reply threading: only the first chunk replies to the original message.
		if i == 0 && replyToMsgID > 0 {
			params.ReplyParameters = &models.ReplyParameters{
				MessageID:                replyToMsgID,
				AllowSendingWithoutReply: true,
			}
		}

		_, err := b.api.SendMessage(ctx, params)
		if err != nil {
			// Fallback: send as plain text.
			b.log.Debug("HTML send failed, falling back to plain text",
				slog.Int64("chat_id", chatID),
				slog.Any("err", err))
			plainChunks := chunkString(text, maxLen)
			for _, pc := range plainChunks {
				if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
					ChatID: chatID,
					Text:   pc,
				}); err != nil {
					b.log.Error("SendMessage failed",
						slog.Int64("chat_id", chatID),
						slog.Any("err", err))
					return
				}
			}
			return
		}
	}
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
	b.replyText(ctx, update.Message.Chat.ID, "pong")
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

// buildChatJID returns the picoclaw canonical "tg:<chat_id>" form. The
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
