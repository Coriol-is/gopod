package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// loginHandler dispatches to the real M6.5 interactive OAuth proxy
// if the runner is available, otherwise falls back to the manual
// docker exec workaround hint.
func (b *Bot) loginHandler(ctx context.Context, api *bot.Bot, update *models.Update) {
	if b.runner != nil {
		b.loginHandlerReal(ctx, api, update)
		return
	}
	// Fallback: no runner (Docker unavailable).
	if update == nil || update.Message == nil {
		return
	}
	b.persistMessage(ctx, update.Message)
	b.replyText(ctx, update.Message.Chat.ID,
		"Runner is not available (Docker may be unreachable). "+
			"Cannot start interactive login.")
}

// whoamiHandler answers /whoami with the chat's id, the operator's
// user id, and the chat's registration state. Useful for the owner
// to discover the chat ids of new chats they want to /register, and
// for non-owner users to confirm their chat is reachable.
func (b *Bot) whoamiHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	m := update.Message
	b.persistMessage(ctx, m)

	chatJID := buildChatJID(m.Chat.ID)
	rc, err := b.store.GetRegistered(ctx, chatJID)

	var sb strings.Builder
	fmt.Fprintf(&sb, "chat_id: %d\n", m.Chat.ID)
	fmt.Fprintf(&sb, "chat_jid: %s\n", chatJID)
	if m.From != nil {
		fmt.Fprintf(&sb, "from_user_id: %d\n", m.From.ID)
	}
	fmt.Fprintf(&sb, "type: %s\n", m.Chat.Type)
	if m.Chat.ID == b.ownerChatID {
		sb.WriteString("owner_candidate: yes (matches GOPOD_OWNER_CHAT_ID)\n")
	} else {
		sb.WriteString("owner_candidate: no\n")
	}

	if err == nil {
		fmt.Fprintf(&sb, "registered: yes\nfolder: %s\nis_owner: %v\n", rc.Folder, rc.IsOwner)
	} else if errors.Is(err, store.ErrChatNotRegistered) {
		sb.WriteString("registered: no\n")
	} else {
		fmt.Fprintf(&sb, "registered: ?? (error: %v)\n", err)
	}

	b.replyText(ctx, m.Chat.ID, sb.String())
}

