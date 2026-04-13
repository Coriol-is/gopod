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

	"github.com/spaceinvaderz/gopod/internal/store"
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

// registerHandler implements the /register command. Owner-only.
//
// Usage:
//
//	/register <chat_id> <folder>
//
// Registers the chat identified by <chat_id> with the on-disk folder
// name <folder>. The folder name is validated against the same regex
// mountsec uses for AllowedChats entries. The owner cannot register
// itself via /register — that path is auto-handled by resolveRegistered
// in handler.go.
func (b *Bot) registerHandler(ctx context.Context, _ *bot.Bot, update *models.Update) {
	if update == nil || update.Message == nil {
		return
	}
	m := update.Message
	b.persistMessage(ctx, m)

	if b.ownerChatID == 0 || m.Chat.ID != b.ownerChatID {
		b.replyText(ctx, m.Chat.ID, "/register is only available in the owner chat.")
		return
	}

	args := parseSlashArgs(messageText(m))
	if len(args) != 2 {
		b.replyText(ctx, m.Chat.ID,
			"Usage: /register <chat_id> <folder>\n\n"+
				"  chat_id  numeric Telegram chat id (run /whoami in the target chat to find it)\n"+
				"  folder   on-disk workspace name, ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$")
		return
	}

	targetID, err := strconv.ParseInt(args[0], 10, 64)
	if err != nil {
		b.replyText(ctx, m.Chat.ID, fmt.Sprintf("bad chat_id %q: %v", args[0], err))
		return
	}
	folder := args[1]

	// Owner cannot re-register itself via /register; that path is
	// reserved for auto-registration with folder=owner.
	if targetID == b.ownerChatID {
		b.replyText(ctx, m.Chat.ID,
			"the owner chat is auto-registered as folder \"owner\"; /register is for other chats.")
		return
	}

	rc := store.RegisteredChat{
		JID:     buildChatJID(targetID),
		Folder:  folder,
		IsOwner: false,
		AddedAt: time.Now().UnixMilli(),
	}
	if err := b.store.RegisterChat(ctx, rc); err != nil {
		b.log.Error("RegisterChat failed",
			slog.Int64("target_id", targetID),
			slog.String("folder", folder),
			slog.Any("err", err))
		b.replyText(ctx, m.Chat.ID, fmt.Sprintf("register failed: %v", err))
		return
	}

	b.log.Info("registered chat via /register",
		slog.Int64("target_id", targetID),
		slog.String("folder", folder))
	b.replyText(ctx, m.Chat.ID, fmt.Sprintf(
		"registered chat %d as folder %q.\n\nNext: send a message in that chat to spawn its agent container.",
		targetID, folder))
}

// parseSlashArgs takes the raw "/cmd arg1 arg2" text and returns the
// args after the command. Whitespace-split on runs of spaces and tabs.
// Strips an optional `@botname` suffix from the command itself so
// `/register@gopodbot 123 foo` parses correctly in groups.
func parseSlashArgs(text string) []string {
	fields := strings.Fields(text)
	if len(fields) == 0 {
		return nil
	}
	// Drop the leading /command (we don't return it).
	return fields[1:]
}
