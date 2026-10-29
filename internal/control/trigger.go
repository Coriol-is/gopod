package control

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const triggerUnavailable = "Trigger setting unavailable."

const triggerUsage = "Usage: /trigger [owner|mention|owner+mention|all]"

// triggerHelp explains what each mode answers and what gopod cannot change.
const triggerHelp = "owner: only the owner's own messages\n" +
	"mention: messages that mention the bot, reply to it or are commands to it\n" +
	"owner+mention: the owner's messages, and others when they address the bot\n" +
	"all: every message the bot receives\n\n" +
	"Telegram decides which messages reach the bot: with privacy mode on, plain " +
	"messages are not delivered, so `all` and `owner` only work when Telegram " +
	"delivers them. gopod never changes privacy mode or admin rights."

// groupTarget resolves the registered group a group-scoped command acts on.
// From a group it is the calling group, whatever the arguments say. From the
// owner's private chat or CLI it is the group named by the first argument.
// rest are the arguments that remain.
func groupTarget(cmd Command) (jid string, rest []string, ok bool) {
	if cmd.Caller.ChatID < 0 {
		return "tg:" + strconv.FormatInt(cmd.Caller.ChatID, 10), cmd.Args, true
	}
	if len(cmd.Args) == 0 {
		return "", nil, false
	}
	text := strings.TrimPrefix(cmd.Args[0], "tg:")
	id, err := strconv.ParseInt(text, 10, 64)
	if err != nil || id >= 0 || text != strconv.FormatInt(id, 10) {
		return "", nil, false
	}
	return "tg:" + text, cmd.Args[1:], true
}

// RegisterTriggerCommand adds /trigger (D048): show or set when the agent
// answers in a registered group. The owner may use it without confirmation
// from the group itself, or from the private chat with the group id. The mode
// is group configuration, not a boundary: it narrows who can start a turn.
func RegisterTriggerCommand(r *Router, st *store.Store) {
	r.Register("trigger", "trigger", "(owner) show or set when the agent answers in a group", PermGroupOwner,
		func(ctx context.Context, cmd Command) (Response, error) {
			jid, rest, ok := groupTarget(cmd)
			if !ok || len(rest) > 1 {
				return Response{Code: 1, Text: triggerUsage + "\nFrom the private chat: /trigger <group_chat_id> [mode]"}, nil
			}
			rc, err := st.GetRegistered(ctx, jid)
			if errors.Is(err, store.ErrChatNotRegistered) || (err == nil && (rc.IsOwner || rc.Folder == "owner")) {
				return Response{Code: 1, Text: "This group is not registered."}, nil
			}
			if err != nil {
				return Response{Code: 1, Text: triggerUnavailable}, fmt.Errorf("trigger: %w", err)
			}
			if len(rest) == 0 {
				return showTrigger(ctx, st, jid, cmd.Caller.ChatID < 0)
			}
			mode, err := store.ParseTriggerMode(rest[0])
			if err != nil {
				return Response{Code: 1, Text: triggerUsage}, nil
			}
			if err := st.SetGroupTrigger(ctx, jid, mode, time.Now().UnixMilli()); err != nil {
				return Response{Code: 1, Text: triggerUnavailable}, fmt.Errorf("trigger set: %w", err)
			}
			return Response{Text: fmt.Sprintf("Trigger mode: %s.", mode), Data: map[string]any{"jid": jid, "mode": string(mode)}}, nil
		})
}

func showTrigger(ctx context.Context, st *store.Store, jid string, fromGroup bool) (Response, error) {
	mode, err := st.GroupTrigger(ctx, jid)
	if err != nil {
		return Response{Code: 1, Text: triggerUnavailable}, fmt.Errorf("trigger show: %w", err)
	}
	return Response{
		Text:    fmt.Sprintf("Trigger mode: %s\n\n%s\n\n%s", mode, triggerUsage, triggerHelp),
		Data:    map[string]any{"jid": jid, "mode": string(mode)},
		Buttons: triggerButtons(jid, mode, fromGroup),
	}, nil
}
