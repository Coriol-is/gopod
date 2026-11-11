package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

type foregroundDeliveryKey struct{}
type foregroundDeliveryBinding struct{ Mode string }

// EnableForegroundDelivery is called only after the dispatcher starts and the
// immutable artifact/speech adapters are installed. The default remains legacy.
// The selection is captured per batch, so later startup/configuration changes
// cannot move a running turn between owners.
func (b *Bot) EnableForegroundDelivery() error {
	if b.store == nil || b.prepared == nil || b.prepared.Scopes == nil || b.prepared.Artifacts == nil || b.prepared.Blobs == nil || b.ownerChatID <= 0 {
		return errors.New("foreground delivery requires owner, store and immutable preparation")
	}
	b.foregroundDelivery.Store(true)
	return nil
}

func (b *Bot) bindForegroundDelivery(ctx context.Context, binding store.OrdinaryStopBinding, item queue.Item) context.Context {
	if !b.foregroundDelivery.Load() || binding.Conversation.Scope.JID != buildChatJID(b.ownerChatID) {
		return ctx
	}
	ref := store.ExecutionRef{Conversation: binding.Conversation, Kind: store.ExecInteractive, Members: binding.Members}
	ctx = runner.WithResultFreeze(ctx, ref, b.store)
	return context.WithValue(ctx, foregroundDeliveryKey{}, foregroundDeliveryBinding{Mode: b.getReplyMode(item.ChatID, item.IsVoice)})
}
func usesForegroundDelivery(ctx context.Context) bool {
	_, enabled := ctx.Value(foregroundDeliveryKey{}).(foregroundDeliveryBinding)
	return enabled
}

type finalPublicationError struct{ cause error }

func (e *finalPublicationError) Error() string {
	return fmt.Sprintf("frozen final reply requires delivery reconciliation: %v", e.cause)
}
func (e *finalPublicationError) Unwrap() error  { return e.cause }
func (e *finalPublicationError) NoReplay() bool { return true }

// publishForeground is called only after final handoff sealed and joined every
// preview writer. The runner has already frozen the result before returning.
// Any preparation/publication failure must hold delivery, never rerun inference.
func (b *Bot) publishForeground(ctx context.Context, item queue.Item, p FinalReplyPresentation) error {
	result, found, err := b.store.TurnResultForAttempt(ctx, item.ID, item.Attempts)
	if err != nil || !found {
		return &finalPublicationError{errors.Join(err, store.ErrFinalReplyNotReady)}
	}
	if result.Conversation.Scope.JID != buildChatJID(item.ChatID) || result.Conversation.Scope.Folder != item.Folder || result.Conversation.ThreadID != item.ThreadID {
		return &finalPublicationError{store.ErrScopeMismatch}
	}
	raw, err := json.Marshal(b.replyMarkup(item, result.Text))
	if err != nil {
		return &finalPublicationError{err}
	}
	if string(raw) != "null" {
		p.ReplyMarkup = string(raw)
	}
	p.Mode = foregroundMode(ctx)
	p.PreviewText = truncateRunes(p.PreviewText, 4000)
	p.ReplyToMessageID = int64(item.MessageID)
	if b.threadGone(item.ChatID, item.ThreadID) {
		b.turnStatus(item).Set(stError)
		return &finalPublicationError{fmt.Errorf("original topic is unavailable; frozen answer retained without rerouting: %w", store.ErrTopicDeleting)}
	}
	b.turnStatus(item).Set(stDeliveryQueued)
	if _, err = b.PrepareFrozenFinalReply(ctx, result.ID, p); err != nil {
		return &finalPublicationError{err}
	}
	// Publication is outbox ownership, not a Telegram ACK. Reply memo, final row
	// authority and delivery transcript are set only by acknowledged transport.
	return nil
}

// ObserveForegroundAcknowledgement reports completion only after the whole
// immutable plan is ACKed. Failure here cannot affect delivery or native replay.
func (b *Bot) ObserveForegroundAcknowledgement(c store.OutboxClaim) {
	if c.ResultID == "" || b.store == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	d, err := b.store.GetDelivery(ctx, c.DeliveryID)
	if err != nil || d.State != store.DeliverySent {
		return
	}
	r, err := b.store.GetTurnResult(ctx, c.ResultID)
	if err != nil || r.Kind != store.ExecInteractive || r.Conversation.Scope.JID != buildChatJID(b.ownerChatID) {
		return
	}
	for _, member := range r.Members {
		turn, err := b.store.GetTurn(ctx, member.TurnID)
		if err != nil || turn.Attempts != member.Attempt {
			continue
		}
		b.turnStatus(queue.Item{ChatID: turn.ChatID, ThreadID: turn.ThreadID, MessageID: int(turn.TGMessageID)}).Set(stDone)
	}
}

func foregroundMode(ctx context.Context) string {
	binding, _ := ctx.Value(foregroundDeliveryKey{}).(foregroundDeliveryBinding)
	return binding.Mode
}
