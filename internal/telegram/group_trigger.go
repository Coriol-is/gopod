package telegram

import (
	"context"
	"errors"
	"log/slog"
	"strings"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// triggerMatches is the trigger decision of D048: whether a message starts a
// turn in a group under mode.
func triggerMatches(mode store.TriggerMode, fromOwner, addressed bool) bool {
	switch mode {
	case store.TriggerOwner:
		return fromOwner
	case store.TriggerMention:
		return addressed
	case store.TriggerOwnerMention:
		return fromOwner || addressed
	case store.TriggerAll:
		return true
	}
	return false
}

// groupTriggerSkips reports whether m is a message of a registered group that
// the group's trigger mode does not answer. Such a message is stored in the chat
// history (not injected into prompts) and then ignored: no turn, no model call,
// no quota use and no notice. Commands follow groupCommandSkips. The owner's
// private chat and chats that are not registered groups are never gated here. A
// registration lookup failure other than "not registered" fails closed.
func (b *Bot) groupTriggerSkips(ctx context.Context, m *models.Message) bool {
	if m == nil || m.Chat.ID >= 0 || !isGroupChat(m.Chat.Type) {
		return false
	}
	jid := buildChatJID(m.Chat.ID)
	rc, err := b.store.GetRegistered(ctx, jid)
	if errors.Is(err, store.ErrChatNotRegistered) {
		return false
	}
	if err != nil {
		b.log.Error("group registration lookup failed; message not answered", slog.String("chat_jid", jid), slog.Any("err", err))
		return true
	}
	if rc.IsOwner {
		return false
	}
	mode, err := b.store.GroupTrigger(ctx, jid)
	if err != nil {
		b.log.Error("group trigger lookup failed; message not answered", slog.String("chat_jid", jid), slog.Any("err", err))
		return true
	}
	if text := strings.TrimSpace(messageText(m)); strings.HasPrefix(text, "/") {
		return b.groupCommandSkips(m, mode, text)
	}
	skip := !triggerMatches(mode, b.isGroupOwner(m), b.addressedOrUncertain(m, mode))
	if skip {
		b.log.Debug("group message outside trigger mode", slog.String("chat_jid", jid), slog.String("mode", string(mode)))
	}
	return skip
}

// groupCommandSkips decides a slash command in a registered group. A command
// addressed to another bot is never ours, in any mode. In gated modes a bare
// /cmd from a member is not addressed to this bot either; the owner's bare
// commands and /cmd@thisbot pass. In `all` mode commands behave as before.
func (b *Bot) groupCommandSkips(m *models.Message, mode store.TriggerMode, text string) bool {
	target := commandTarget(text)
	username := b.botUsername()
	if target != "" && username != "" && !equalFoldASCII(target, username) {
		return true
	}
	if mode == store.TriggerOwner {
		return !b.isGroupOwner(m)
	}
	if mode == store.TriggerAll || target != "" {
		return false
	}
	return !b.isGroupOwner(m)
}

// commandTarget returns the bot named by the "@suffix" of the first word of a
// command message, or "".
func commandTarget(text string) string {
	word := text
	if i := strings.IndexAny(word, " \t\n"); i >= 0 {
		word = word[:i]
	}
	if i := strings.IndexByte(word, '@'); i >= 0 {
		return word[i+1:]
	}
	return ""
}

// silentForMember reports whether a command reply (such as "unknown command")
// must be withheld from m: a member of a registered group in a gated mode.
func (b *Bot) silentForMember(ctx context.Context, m *models.Message) bool {
	if m == nil || m.Chat.ID >= 0 || !isGroupChat(m.Chat.Type) || b.isGroupOwner(m) {
		return false
	}
	rc, err := b.store.GetRegistered(ctx, buildChatJID(m.Chat.ID))
	if err != nil || rc.IsOwner {
		return false
	}
	mode, err := b.store.GroupTrigger(ctx, rc.JID)
	return err == nil && mode != store.TriggerAll
}

// addressedOrUncertain is addressesBot, except while the bot's username is still
// unknown (getMe has not succeeded): then a message that carries any mention
// entity or replies to a bot counts as addressed, so an addressed message is
// never silently dropped. The gate recovers on its own once the username arrives.
func (b *Bot) addressedOrUncertain(m *models.Message, mode store.TriggerMode) bool {
	if b.addressesBot(m) {
		return true
	}
	if mode == store.TriggerAll || mode == store.TriggerOwner || b.botUsername() != "" || !maybeAddressesBot(m) {
		return false
	}
	b.log.Warn("bot username unknown: treating a possibly addressed group message as addressed", slog.Int64("chat_id", m.Chat.ID))
	if b.capabilities != nil {
		go b.capabilities.Refresh(context.Background()) // rate limited by its own cooldown
	}
	return true
}

func (b *Bot) botUsername() string {
	if b.capabilities == nil {
		return ""
	}
	return b.capabilities.Snapshot().Username
}

// maybeAddressesBot is true for any mention-like entity or a reply to a bot.
func maybeAddressesBot(m *models.Message) bool {
	if m == nil || m.ForwardOrigin != nil {
		return false
	}
	if r := m.ReplyToMessage; r != nil && r.From != nil && r.From.IsBot {
		return true
	}
	for _, list := range [][]models.MessageEntity{m.Entities, m.CaptionEntities} {
		for _, e := range list {
			switch e.Type {
			case models.MessageEntityTypeMention, models.MessageEntityTypeTextMention, models.MessageEntityTypeBotCommand:
				return true
			}
		}
	}
	return false
}

// withGroupNote puts the host's group line above a group turn (D048): the
// current trigger mode and whether the sender is the group's owner. The agent is
// told in its instructions that members other than the owner are untrusted and
// that only this first line is host-written. Private chats get no line.
func (b *Bot) withGroupNote(ctx context.Context, m *models.Message, rc store.RegisteredChat, text string) string {
	if m == nil || m.Chat.ID >= 0 || rc.IsOwner || !isGroupChat(m.Chat.Type) {
		return text
	}
	mode, err := b.store.GroupTrigger(ctx, rc.JID)
	if err != nil {
		return text
	}
	sender := "member"
	if b.isGroupOwner(m) {
		sender = "owner"
	}
	return "[gopod group: trigger=" + string(mode) + "; sender=" + sender + "]\n\n" + text
}

// A button press addresses the bot, but does not inherit its original sender.
func (b *Bot) groupActionAllowed(ctx context.Context, q cbQuery) bool {
	if q.ChatID >= 0 {
		return true
	}
	if b.store == nil {
		return false
	}
	rc, err := b.store.GetRegistered(ctx, buildChatJID(q.ChatID))
	if err != nil || rc.IsOwner || rc.Folder != q.Chat.Folder {
		return false
	}
	mode, err := b.store.GroupTrigger(ctx, rc.JID)
	return err == nil && triggerMatches(mode, b.isGroupOwnerUser(q.ChatKind, q.ChatID, q.UserID), true)
}

func (b *Bot) callbackPrompt(ctx context.Context, q cbQuery, text string) string {
	if q.ChatID >= 0 {
		return text
	}
	regen := strings.HasPrefix(text, regeneratePrefix)
	text = strings.TrimPrefix(text, regeneratePrefix)
	if strings.HasPrefix(text, "[gopod group: ") {
		if _, rest, ok := strings.Cut(text, "\n\n"); ok {
			text = rest
		}
	}
	if regen {
		text = regeneratePrefix + text
	}
	m := &models.Message{Chat: models.Chat{ID: q.ChatID, Type: models.ChatType(q.ChatKind)}, From: &models.User{ID: q.UserID}}
	return b.withGroupNote(ctx, m, q.Chat, text)
}
