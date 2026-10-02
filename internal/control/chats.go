package control

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// RegisterChatCommands adds the chat-registration commands to the
// Router. Owner-only: registering a chat grants it an agent container
// and its own workspace folder, so it is a privilege boundary.
//
// Usage:
//
//	/register <chat_id> <folder>
//
// chat_id is the numeric Telegram chat id (/whoami in the target chat
// prints it). folder is the on-disk workspace name, validated by
// store.RegisterChat against the same regex mountsec uses for
// AllowedChats entries.
//
// The owner chat is auto-registered as folder "owner" by
// telegram.resolveRegistered and cannot be re-registered here.
func RegisterChatCommands(r *Router, st *store.Store, ownerChatID int64) {
	r.Register("chats.register", "register", "(owner) register a chat for agent access",
		PermOwnerOnly, chatsRegister(st, ownerChatID))
}

func chatsRegister(st *store.Store, ownerChatID int64) Handler {
	return func(ctx context.Context, cmd Command) (Response, error) {
		if len(cmd.Args) != 2 {
			return Response{
				Text: "Usage: /register <chat_id> <folder>\n\n" +
					"  chat_id  numeric Telegram chat id (run /whoami in the target chat to find it)\n" +
					"  folder   on-disk workspace name, ^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$",
				Code: 1,
			}, nil
		}

		targetID, err := strconv.ParseInt(cmd.Args[0], 10, 64)
		if err != nil {
			return Response{
				Text: fmt.Sprintf("bad chat_id %q: not a number", cmd.Args[0]),
				Code: 1,
			}, nil
		}
		folder := cmd.Args[1]

		if ownerChatID != 0 && targetID == ownerChatID {
			return Response{
				Text: "the owner chat is auto-registered as folder \"owner\"; /register is for other chats.",
				Code: 1,
			}, nil
		}

		rc := store.RegisteredChat{
			JID:     chatJID(targetID),
			Folder:  folder,
			IsOwner: false,
			AddedAt: time.Now().UnixMilli(),
		}
		if err := st.RegisterChat(ctx, rc); err != nil {
			return Response{
				Text: fmt.Sprintf("register failed: %v", err),
				Code: 1,
			}, fmt.Errorf("registering chat %d as %q: %w", targetID, folder, err)
		}

		return Response{
			Text: fmt.Sprintf(
				"registered chat %d as folder %q.\n\nNext: send a message in that chat to spawn its agent container.",
				targetID, folder),
			Data: map[string]any{
				"chat_id": targetID,
				"folder":  folder,
				"jid":     rc.JID,
			},
		}, nil
	}
}

// chatJID returns the gopod canonical "tg:<chat_id>" form, matching
// telegram.buildChatJID.
func chatJID(chatID int64) string {
	return "tg:" + strconv.FormatInt(chatID, 10)
}
