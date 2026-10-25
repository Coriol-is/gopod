package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// offerProviderFallback offers a one-shot, explicit switch. No provider or queue
// mutation happens until the Yes button passes back through the Router.
func (b *Bot) offerProviderFallback(ctx context.Context, item queue.Item, runErr error) bool {
	var quota *runner.UsageLimitError
	if !errors.As(runErr, &quota) || !b.canRetry(item) || b.router == nil {
		return false
	}
	target := ""
	switch quota.Provider {
	case "claude":
		target = "codex"
	case "codex":
		target = "claude"
	default:
		return false
	}
	turn, err := b.store.GetTurn(ctx, item.ID)
	if err != nil || strings.HasPrefix(turn.SourceID, "fallback:") {
		return false
	}
	cmd := control.Command{Name: "provider.fallback", Args: []string{strconv.FormatInt(item.ID, 10), quota.Provider, target}, Caller: control.Caller{ChatID: item.ChatID, ThreadID: item.ThreadID, IsOwner: item.IsOwner, Source: control.SourceTelegram}}
	if _, err := b.router.Check(cmd); err != nil {
		return false
	}
	if err := b.store.SetState(ctx, control.QuotaOfferKey(item.ID), quota.Provider); err != nil {
		b.log.Warn("persist quota offer", "turn", item.ID, "err", err)
		return false
	}
	resp := control.Confirm(cmd, fmt.Sprintf(quotaSwitchQuestion, quota.Provider, target))
	resp.Buttons[0][0].Label = btnSwitchContinue
	resp.Buttons[0][1].Label = btnFallbackCancel
	b.sendCommandResponse(ctx, item.ChatID, item.ThreadID, item.MessageID, resp)
	return true
}

// EnqueueProviderFallback persists one retry identity per original turn, so
// double taps/redelivery never create multiple retries. It does not run a model.
func (b *Bot) EnqueueProviderFallback(ctx context.Context, t store.Turn) (bool, error) {
	if b.queue == nil {
		return false, errors.New("telegram: retry queue unavailable")
	}
	item := queue.Item{ChatID: t.ChatID, MessageID: int(t.TGMessageID), ThreadID: t.ThreadID, Folder: t.ChatFolder, IsOwner: t.IsOwner, Text: t.Text, FilePath: t.FilePath, IsVoice: t.IsVoice, Source: "telegram", SourceID: control.FallbackSourceID(t.ID)}
	id, dup := b.queue.Enqueue(ctx, item)
	if dup {
		return true, nil
	}
	if id == 0 {
		return false, errors.New("telegram: fallback retry not persisted")
	}
	b.newStatusReactor(item.ChatID, item.MessageID).Set(stQueued)
	return false, nil
}
