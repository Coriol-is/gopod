package telegram

import (
	"context"
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
// the group's trigger mode does not answer. Such a message was already stored as
// chat context; it gets no turn, no model call, no quota use and no notice.
// Commands are not turns and always pass. The owner's private chat and chats
// that are not registered groups are never gated here.
func (b *Bot) groupTriggerSkips(ctx context.Context, m *models.Message) bool {
	if m == nil || m.Chat.ID >= 0 || !isGroupChat(m.Chat.Type) || strings.HasPrefix(strings.TrimSpace(messageText(m)), "/") {
		return false
	}
	jid := buildChatJID(m.Chat.ID)
	rc, err := b.store.GetRegistered(ctx, jid)
	if err != nil || rc.IsOwner {
		return false
	}
	mode, err := b.store.GroupTrigger(ctx, jid)
	if err != nil {
		b.log.Error("group trigger lookup failed; message not answered", slog.String("chat_jid", jid), slog.Any("err", err))
		return true
	}
	skip := !triggerMatches(mode, b.isGroupOwner(m), b.addressesBot(m))
	if skip {
		b.log.Debug("group message outside trigger mode", slog.String("chat_jid", jid), slog.String("mode", string(mode)))
	}
	return skip
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
