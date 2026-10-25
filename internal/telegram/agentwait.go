package telegram

// Waiting for the agent gate (runner/gate.go, D026). When a streamed
// turn cannot start its agent at once (all agent slots taken, or the
// host is short of memory), the runner calls the turn's WaitNotice once,
// before it blocks. The notice shows the wait where the turn's output
// will appear:
//
//   - draft path: one draft with the wait text, bound to the turn so the
//     Stop button already works. A refresher re-sends it every
//     draftRefresh (a draft expires ~30s after its last update, taking the
//     Stop button with it) until runAgentStreaming stops it, right after
//     RunStream returns and before the opening draft of streamReply (a new
//     draft id) replaces it.
//   - edit path: the placeholder is sent early with the wait text and
//     handed to streamReply, which turns it back into the cursor when the
//     agent starts and then edits it as usual.

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
)

// Texts shown while a turn waits for the agent gate.
const (
	waitSlotText   = "⏳ ждёт свободный слот агента…"
	waitMemoryText = "⏳ ждёт свободную память…"
)

func waitText(r runner.WaitReason) string {
	if r == runner.WaitMemory {
		return waitMemoryText
	}
	return waitSlotText
}

// waitKey names one turn's early placeholder.
type waitKey struct {
	turn   int64
	chat   int64
	msg    int
	thread int
}

func waitKeyOf(item queue.Item) waitKey {
	return waitKey{item.ID, item.ChatID, item.MessageID, item.ThreadID}
}

// waitNotice returns the runner.WaitNotice of one streamed turn. ctx is
// the turn's Telegram context (not the agent's run context).
func (b *Bot) waitNotice(ctx context.Context, item queue.Item) runner.WaitNotice {
	return func(reason runner.WaitReason) {
		text := waitText(reason)
		if item.Resumed && item.PlaceholderMsgID != 0 {
			// streamReply reuses the interrupted attempt's placeholder.
			b.editMessage(ctx, item.ChatID, item.PlaceholderMsgID, text)
			return
		}
		if b.draftsSupported(item.ChatID) {
			stoppable := b.inflightStop(item.ID) != nil
			id := b.newDraftID()
			b.bindDraft(item.ID, item.ChatID, id)
			opts := draftOpts{CanStop: stoppable, KeepOnStop: stoppable}
			err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, id, text, opts)
			if err == nil {
				b.startWaitDraftRefresh(ctx, item, id, text, opts, time.Time{})
				return
			}
			if b.draftsSupported(item.ChatID) && !errors.Is(err, errThreadGone) {
				// Transient: the refresher retries it.
				b.log.Debug("wait draft failed",
					slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
				var hold time.Time
				if wait, ok := retryAfter(err); ok {
					hold = time.Now().Add(wait)
				}
				b.startWaitDraftRefresh(ctx, item, id, text, opts, hold)
				return
			}
			b.bindDraft(item.ID, item.ChatID, 0)
		}
		ph := b.sendPlaceholderText(ctx, item.ChatID, item.ThreadID, item.MessageID, text)
		if ph == 0 {
			return
		}
		b.recordPlaceholder(ctx, item, ph)
		b.waitMu.Lock()
		if b.waitPlaceholders == nil {
			b.waitPlaceholders = make(map[waitKey]int)
		}
		b.waitPlaceholders[waitKeyOf(item)] = ph
		b.waitMu.Unlock()
	}
}

// popWaitPlaceholder returns (and forgets) the placeholder the turn's
// wait notice sent, or 0.
func (b *Bot) popWaitPlaceholder(item queue.Item) int {
	b.waitMu.Lock()
	defer b.waitMu.Unlock()
	k := waitKeyOf(item)
	ph := b.waitPlaceholders[k]
	delete(b.waitPlaceholders, k)
	return ph
}

// takeWaitPlaceholder hands the turn's early placeholder to streamReply,
// back to the cursor now that the agent runs. 0 when there is none.
func (b *Bot) takeWaitPlaceholder(ctx context.Context, item queue.Item) int {
	ph := b.popWaitPlaceholder(item)
	if ph != 0 {
		b.editMessage(ctx, item.ChatID, ph, "")
	}
	return ph
}

// streamNotStarted ends a turn whose agent never started because
// runCtx was cancelled while it waited (for the chat's previous agent or
// for the agent gate). A Stop ends it stopped, with stoppedEmpty as the
// reply (in the early placeholder when there is one); a shutdown returns
// nil and lets the queue mark the turn interrupted. handled is false
// when runCtx is still live: a real failure, which the caller handles.
func (b *Bot) streamNotStarted(ctx, runCtx context.Context, item queue.Item) (bool, error) {
	if runCtx.Err() == nil {
		return false, nil
	}
	ph := b.popWaitPlaceholder(item)
	if ctx.Err() != nil {
		return true, nil
	}
	b.log.Info("turn stopped before its agent started",
		slog.Int64("turn", item.ID), slog.String("folder", item.Folder))
	sent := ph
	if ph != 0 {
		b.editMessage(ctx, item.ChatID, ph, stoppedEmpty)
	} else {
		sent = b.sendFormatted(ctx, item.ChatID, item.ThreadID, item.MessageID, stoppedEmpty)
	}
	b.recordReply(ctx, item, sent, stoppedEmpty)
	if item.MessageID > 0 {
		b.react(ctx, item.ChatID, item.MessageID, emojiDone)
	}
	return true, queue.ErrStopped
}

// startWaitDraftRefresh keeps the turn's wait draft alive: it re-sends
// the same draft id and text every draftRefresh (b.waitDraftEvery in
// tests), skipping sends before a 429's retry_after, until
// stopWaitDraftRefresh or ctx ends. It gives up when drafts are refused
// or the topic is gone.
func (b *Bot) startWaitDraftRefresh(ctx context.Context, item queue.Item, draftID int, text string, opts draftOpts, hold time.Time) {
	every := b.waitDraftEvery
	if every <= 0 {
		every = draftRefresh
	}
	quit := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(every)
		defer ticker.Stop()
		for {
			select {
			case <-quit:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
			if time.Now().Before(hold) {
				continue // rate limited: wait out retry_after
			}
			err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, draftID, text, opts)
			if err == nil {
				continue
			}
			if wait, ok := retryAfter(err); ok {
				hold = time.Now().Add(wait)
			}
			if !b.draftsSupported(item.ChatID) || errors.Is(err, errThreadGone) {
				return
			}
			b.log.Debug("wait draft refresh failed",
				slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
		}
	}()
	b.waitMu.Lock()
	if b.waitRefresh == nil {
		b.waitRefresh = make(map[waitKey]waitRefresher)
	}
	b.waitRefresh[waitKeyOf(item)] = waitRefresher{quit: quit, done: done}
	b.waitMu.Unlock()
}

// waitRefresher is a running wait draft refresher: close quit to stop
// it; done closes when its goroutine has exited.
type waitRefresher struct {
	quit chan struct{}
	done chan struct{}
}

// stopWaitDraftRefresh stops the turn's wait draft refresher, if any,
// and returns once it has exited, so no refresh can land after the
// stream's first draft.
func (b *Bot) stopWaitDraftRefresh(item queue.Item) {
	b.waitMu.Lock()
	k := waitKeyOf(item)
	rf, ok := b.waitRefresh[k]
	delete(b.waitRefresh, k)
	b.waitMu.Unlock()
	if ok {
		close(rf.quit)
		<-rf.done
	}
}
