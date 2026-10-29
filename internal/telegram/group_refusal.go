package telegram

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// groupRefusal reports whether err means the group's agent runtime is not
// available (not admitted, provider login missing or expired, usage limit). The
// group is told one neutral line; the owner gets the precise cause privately.
func groupRefusal(err error) bool {
	return errors.Is(err, runner.ErrProfileUnavailable) || errors.Is(err, runner.ErrUsageLimit) ||
		errors.Is(err, runner.ErrSessionExpired) || errors.Is(err, runner.ErrNotLoggedIn)
}

func refusalCause(err error) string {
	switch {
	case errors.Is(err, runner.ErrUsageLimit):
		return "provider usage limit reached"
	case errors.Is(err, runner.ErrSessionExpired):
		return "provider login expired"
	case errors.Is(err, runner.ErrNotLoggedIn):
		return "provider not logged in"
	}
	return "group runtime not admitted"
}

// replyGroupRefusal answers a refused group turn without internals: no provider,
// account, path or error text reaches the group, and no login or retry button is
// offered there. Both notices are rate limited per group.
func (b *Bot) replyGroupRefusal(ctx context.Context, item queue.Item, kind errKind, err error) {
	b.log.Warn("group agent unavailable", slog.String("chat_folder", item.Folder), slog.Int64("turn", item.ID), slog.String("kind", kind.String()), slog.String("cause", refusalCause(err)))
	if b.errCooldownPass(item.ChatID, errGroupUnavailable, 0) {
		b.sendErrorReply(ctx, item, errTextGroupUnavailable, nil)
	}
	if b.ownerChatID <= 0 || !b.errCooldownPass(item.ChatID, errGroupOwnerNotice, 0) {
		return
	}
	reason := ""
	if b.groupReason != nil {
		reason = b.groupReason(ctx, buildChatJID(item.ChatID))
	}
	text := fmt.Sprintf("Group %s: the agent refused a message (%s).", buildChatJID(item.ChatID), refusalCause(err))
	if reason != "" {
		text += " State: " + reason + "."
	}
	text += " Details: /isolation status " + buildChatJID(item.ChatID)
	b.NotifyOwner(ctx, text)
}

// NotifyOwner sends a private notice to the owner chat. It is best effort:
// failures are logged and never reach any group.
func (b *Bot) NotifyOwner(ctx context.Context, text string) {
	if b == nil || b.ownerChatID <= 0 || b.api == nil {
		return
	}
	if err := b.sendNotice(ctx, b.ownerChatID, 0, text); err != nil {
		b.log.Warn("owner notice failed", slog.Any("err", err))
	}
}
