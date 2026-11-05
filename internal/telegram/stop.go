package telegram

// Stop button. A streaming draft is sent with can_stop, so the user sees
// a stop button; pressing it delivers Update.stopped_message_generation
// carrying (chat, message_thread_id, draft_id). Draft ids are a
// process-local counter (newDraftID), so the in-flight registry below
// records which turn owns which (chat, draft) and maps the update back
// to its turn. D066 persists the complete ordinary batch before native cancel.
// The sender acknowledges Stop without sending new answer text or a reply memo;
// late completion remains separate evidence, never native exit proof.
//
// The edit path (groups, chats that refuse drafts, the gate wait
// placeholder) has no native Stop: its placeholder carries an inline
// ⏹ button (stopKeyboard, data st:<turnID>) whose callback calls
// stopTurn, the same cancel keyed by turn id and authorized by chat.

import (
	"context"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/google/uuid"
	"log/slog"
	"sync"

	"github.com/go-telegram/bot/models"
)

// inflightTurn is one streaming turn that can be stopped.
type inflightTurn struct {
	scope             store.ScopeRef
	token             string
	messages          map[int]bool
	cancel            context.CancelFunc
	stopped           chan struct{} // closed on the first stop
	phaseMu           sync.Mutex
	previewMu         sync.RWMutex
	finalizing        bool
	retired           bool
	stopPending       chan struct{}
	stopErr           error
	intentID          string
	persistStop       func(context.Context) (string, error)
	persistCompletion func(context.Context, string, runner.RunResult) error
	completion        *runner.RunResult

	// home is the chat the turn answers in; only a button in that chat
	// may stop it (stopTurn).
	home   int64
	thread int

	// chatID and draftID name the draft currently showing the stop
	// button; zero when the turn is not streaming through a draft.
	chatID  int64
	draftID int
}

// registerInflight makes turnID, answering in chatID, stoppable until
// unregister is called. cancel stops the turn's agent run. A zero
// turnID (memory-only queue) is not registered, and its drafts and
// placeholders carry no stop button.
func (b *Bot) registerInflight(turnID int64, chatID int64, threadID int, cancel context.CancelFunc) (unregister func()) {
	_, unregister = b.registerInflightTurn(turnID, chatID, threadID, cancel)
	return unregister
}

func (b *Bot) registerInflightTurn(turnID int64, chatID int64, threadID int, cancel context.CancelFunc, configure ...func(*inflightTurn)) (*inflightTurn, func()) {
	if turnID == 0 {
		return nil, func() {}
	}
	t := &inflightTurn{token: uuid.NewString(), messages: make(map[int]bool), cancel: cancel, stopped: make(chan struct{}), home: chatID, thread: threadID}
	for _, setup := range configure {
		setup(t)
	}
	b.inflightMu.Lock()
	if b.inflight == nil {
		b.inflight = make(map[int64]*inflightTurn)
	}
	if prior := b.inflight[turnID]; prior != nil {
		prior.retire()
	}
	b.inflight[turnID] = t
	b.inflightMu.Unlock()
	return t, func() {
		b.inflightMu.Lock()
		defer b.inflightMu.Unlock()
		t.retire()
		if b.inflight[turnID] == t {
			delete(b.inflight, turnID)
		}
	}
}

// inflightStop returns the channel closed when turnID is stopped, or
// nil when the turn is not registered.
func (b *Bot) inflightStop(turnID int64) <-chan struct{} {
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	if t := b.inflight[turnID]; t != nil {
		return t.stopped
	}
	return nil
}

// generationLocked resolves a captured registration without retargeting its
// successor. Callers without a binding retain the legacy lookup; an explicitly
// nil or mismatched binding must never acquire a new registration.
// The caller holds inflightMu.
func (b *Bot) generationLocked(ctx context.Context, turnID int64) *inflightTurn {
	if binding, ok := ctx.Value(finalGenerationKey{}).(finalGeneration); ok {
		if binding.turnID != turnID || b.inflight[turnID] != binding.turn {
			return nil
		}
		return binding.turn
	}
	return b.inflight[turnID]
}

// bindDraft binds only the captured, current generation. A late bind or clear
// from a retired handler cannot replace the successor's Stop target.
func (b *Bot) bindDraft(ctx context.Context, turnID int64, chatID int64, draftID int) {
	b.inflightMu.Lock()
	defer b.inflightMu.Unlock()
	if t := b.generationLocked(ctx, turnID); t != nil {
		t.phaseMu.Lock()
		defer t.phaseMu.Unlock()
		if t.retired || t.finalizing {
			return
		}
		t.chatID, t.draftID = chatID, draftID
	}
}

// handleGenerationStopped handles the user pressing Stop on a draft. It
// stops the turn streaming through that draft in that chat; a stop for
// a finished turn, an unknown draft, or a second stop is a no-op.
func (b *Bot) handleGenerationStopped(ctx context.Context, upd *models.Update) {
	if upd == nil || upd.StoppedMessageGeneration == nil {
		return
	}
	ev := upd.StoppedMessageGeneration
	b.inflightMu.Lock()
	var turnID int64
	var match *inflightTurn
	for id, t := range b.inflight {
		if t.draftID != 0 && t.draftID == ev.DraftID && t.chatID == ev.Chat.ID {
			turnID, match = id, t
			break
		}
	}
	b.inflightMu.Unlock()

	if match == nil {
		b.log.DebugContext(ctx, "stop for no in-flight turn",
			slog.Int64("chat_id", ev.Chat.ID),
			slog.Int("draft_id", ev.DraftID))
		return
	}
	if outcome := match.requestStop(ctx); outcome == control.StopAccepted {
		b.log.InfoContext(ctx, "turn stopped by user",
			slog.Int64("turn", turnID),
			slog.Int64("chat_id", ev.Chat.ID),
			slog.Int("draft_id", ev.DraftID))
	} else if outcome == control.StopFailed {
		b.log.ErrorContext(ctx, "Stop persistence unconfirmed", slog.Int64("turn", turnID))
		b.sendFormatted(ctx, ev.Chat.ID, match.thread, 0, "Stop unconfirmed; answer held for reconciliation.")
	}
}

// stop stops the turn once: it closes stopped and cancels the run.
// Reports whether this call stopped it (false: already stopped).
func (t *inflightTurn) stop() bool {
	return t.requestStop(context.Background()) == control.StopAccepted
}

// stopTurn stops in-flight turn turnID when it answers in chatID, as the
// draft Stop does. Reports false when the turn is not running (finished,
// unknown), belongs to another chat, or was already stopped.
func (b *Bot) stopTurn(turnID int64, chatID int64) bool {
	b.inflightMu.Lock()
	t := b.inflight[turnID]
	b.inflightMu.Unlock()
	if t == nil || t.home != chatID {
		return false
	}
	if !t.stop() {
		return false
	}
	b.log.Info("turn stopped by user (button)",
		slog.Int64("turn", turnID), slog.Int64("chat_id", chatID))
	return true
}

// StopCurrentTurn is the control Router's /stop action. Snapshot the exact
// in-flight turn: a late stop must never be retargeted to its successor.
// Authorization belongs to the Router; this lookup scopes effects to chat/topic.
func (b *Bot) StopCurrentTurn(chatID int64, threadID int) bool {
	return b.RequestStopCurrentTurn(context.Background(), chatID, threadID) == control.StopAccepted
}
func (b *Bot) RequestStopCurrentTurn(ctx context.Context, chatID int64, threadID int) control.StopOutcome {
	b.inflightMu.Lock()
	var match *inflightTurn
	var turnID int64
	for id, t := range b.inflight {
		if t.home == chatID && t.thread == threadID {
			match, turnID = t, id
			break
		}
	}
	b.inflightMu.Unlock()
	if match == nil {
		return control.StopFinished
	}
	outcome := match.requestStop(ctx)
	if outcome != control.StopAccepted {
		return outcome
	}
	b.log.Info("turn stopped by user (command)", slog.Int64("turn", turnID), slog.Int64("chat_id", chatID), slog.Int("thread", threadID))
	return control.StopAccepted
}
