package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

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

	// Skip empty / non-text messages — media handling lands in I2/I3/I4.
	text := strings.TrimSpace(messageText(m))
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

	// Slash commands have their own registered handlers; the default
	// handler still sees commands picoclaw doesn't recognise. Don't
	// send those to the agent — that would be a confusing UX.
	if strings.HasPrefix(text, "/") {
		b.replyText(ctx, m.Chat.ID, "unknown command")
		return
	}

	rc, ok := b.resolveRegistered(ctx, m)
	if !ok {
		// Unregistered non-owner chat. Stay silent on text; the user
		// can still see /ping work and /whoami tells them their id.
		return
	}

	tier := runner.TierRegistered
	if rc.IsOwner {
		tier = runner.TierOwner
	}

	// Show "typing..." in Telegram while the agent runs. Chat actions
	// expire after ~5s, so refresh in a goroutine until the agent
	// returns.
	stopTyping := b.startTyping(ctx, m.Chat.ID)
	defer stopTyping()

	reply, err := b.runner.Run(ctx, rc.Folder, tier, b.allowlist, text)
	if err != nil {
		if errors.Is(err, runner.ErrSessionExpired) {
			b.replyText(ctx, m.Chat.ID,
				"Your authentication session has expired.\n\n"+
					"Run /login to re-authenticate with your Anthropic "+
					"Pro/Max account.")
			return
		}
		if errors.Is(err, runner.ErrNotLoggedIn) {
			b.replyText(ctx, m.Chat.ID,
				"This chat's agent is not authenticated yet.\n\n"+
					"Run /login to sign in with your Anthropic Pro/Max "+
					"account. Credentials persist across container restarts.")
			return
		}
		b.log.Error("runner.Run failed",
			slog.String("chat_folder", rc.Folder),
			slog.Any("err", err))
		b.replyText(ctx, m.Chat.ID, fmt.Sprintf("Sorry, the agent failed: %v", err))
		return
	}

	if reply == "" {
		reply = "(empty reply)"
	}
	b.replyText(ctx, m.Chat.ID, reply)
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

// replyText is a small helper that wraps SendMessage with the standard
// error log path. Splits long messages on a 4096-byte boundary
// (Telegram's hard limit) by chunking — naive byte split, not
// markdown-aware. Markdown chunking is part of I5 / M3.5.
func (b *Bot) replyText(ctx context.Context, chatID int64, text string) {
	const maxLen = 4000 // a bit under 4096 to leave room for Telegram overhead
	chunks := chunkString(text, maxLen)
	for _, c := range chunks {
		if _, err := b.api.SendMessage(ctx, &bot.SendMessageParams{
			ChatID: chatID,
			Text:   c,
		}); err != nil {
			b.log.Error("SendMessage failed",
				slog.Int64("chat_id", chatID),
				slog.Any("err", err))
			return
		}
	}
}

// chunkString splits s into byte chunks of at most n bytes each. UTF-8
// safe at boundaries only when the underlying string is ASCII or when
// the boundary happens to fall outside a multi-byte sequence; for the
// M6 single-shot replies the agent produces this is good enough. A
// proper rune-aware splitter lands with I5.
func chunkString(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	for len(s) > n {
		// Walk back from n to the previous newline so we don't split
		// in the middle of a line when possible.
		split := n
		if i := strings.LastIndex(s[:n], "\n"); i > n/2 {
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
