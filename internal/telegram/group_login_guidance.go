package telegram

import (
	"context"
	"log/slog"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	// groupLoginOwnerNote tells the owner the real step. Groups have no sign-in
	// of their own: the group runtime reads the owner's account, read-only.
	groupLoginOwnerNote = "A group has no sign-in of its own: its agent uses your account. " +
		"To refresh that account, choose the provider with /provider <name> and then send /login " +
		"here in this private chat."
)

// groupLoginGuidance replaces the generic failure of a confirmed group sign-in
// (D048): the owner gets the real step privately, the group a neutral line.
func (b *Bot) groupLoginGuidance(ctx context.Context, ownerChat int64, thread int, rc store.RegisteredChat) {
	b.log.Info("group sign-in redirected to the owner account", slog.String("folder", rc.Folder))
	note := groupLoginOwnerNote
	reason := ""
	if b.groupReason != nil {
		reason = b.groupReason(ctx, rc.JID)
	}
	if b.groupReason == nil || reason != "" {
		// The group is not admitted (or the cause is unknown): the account is
		// not the problem, so do not send the owner down the wrong path.
		note = "Group sign-in could not start"
		if reason != "" {
			note += " (" + reason + ")"
		}
		note += ". Check /isolation status " + rc.JID + "."
	}
	b.replyLoginText(ctx, ownerChat, thread, note)
	groupChat, err := strconv.ParseInt(strings.TrimPrefix(rc.JID, "tg:"), 10, 64)
	if err != nil || groupChat >= 0 {
		return
	}
	b.replyText(ctx, groupChat, 0, groupLoginNeutral)
}
