package telegram

// Status header (spec F6): one line above the tool steps in the draft
// and in the edit-path placeholder, "⏳ 0:42 · 5 шагов" while the agent
// works, dropped as soon as answer text streams. A turn that waits
// behind another conversation of the chat shows "в очереди: 2-й" in a
// draft of its own until it starts. The header carries no model output
// and no paths: only elapsed time, a step count and a queue position.

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
)

const (
	// headerQuiet is how long a turn with no steps and no text shows
	// only the cursor, so quick replies never flash a header.
	headerQuiet = 5 * time.Second
	// headerEditEvery is the edit path's header-only edit interval: an
	// edit costs rate-limit budget, a draft does not.
	headerEditEvery = 5 * time.Second
)

// statusHeader renders the header line (without a newline). pos > 0 is
// a queue position and wins over the rest.
func statusHeader(elapsed time.Duration, steps int, pos int) string {
	if pos > 0 {
		return fmt.Sprintf(headerQueued, pos)
	}
	if elapsed < 0 {
		elapsed = 0
	}
	sec := int(elapsed / time.Second)
	clock := fmt.Sprintf("%d:%02d", sec/60, sec%60)
	if sec >= 3600 {
		clock = fmt.Sprintf("%d:%02d:%02d", sec/3600, sec/60%60, sec%60)
	}
	if steps <= 0 {
		return "⏳ " + clock
	}
	return fmt.Sprintf("⏳ %s · %d %s", clock, steps, stepsWord(steps))
}

// stepsWord is the Russian plural of "шаг" for n.
func stepsWord(n int) string {
	switch {
	case n%100 >= 11 && n%100 <= 14:
		return headerSteps5
	case n%10 == 1:
		return headerSteps1
	case n%10 >= 2 && n%10 <= 4:
		return headerSteps2
	}
	return headerSteps5
}

// statusNow is the clock of the header and of the reactions (fake in
// tests). time.Now carries a monotonic reading, so elapsed times taken
// with it ignore wall-clock jumps.
func (b *Bot) statusNow() time.Time {
	if b.reactClock != nil {
		return b.reactClock.Now()
	}
	return time.Now()
}

// markThreadBusy records that a turn streams in the item's chat thread
// until the returned func is called; queue drafts stay out of it.
func (b *Bot) markThreadBusy(item queue.Item) func() {
	k := chatThread{item.ChatID, item.ThreadID}
	b.statusMu.Lock()
	if b.busyThreads == nil {
		b.busyThreads = make(map[chatThread]int)
	}
	b.busyThreads[k]++
	b.statusMu.Unlock()
	return func() {
		b.statusMu.Lock()
		if b.busyThreads[k]--; b.busyThreads[k] <= 0 {
			delete(b.busyThreads, k)
		}
		b.statusMu.Unlock()
	}
}

func (b *Bot) threadBusy(chatID int64, thread int) bool {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	return b.busyThreads[chatThread{chatID, thread}] > 0
}

// queueWaiter is one waiting turn's queue-position draft loop.
type queueWaiter struct {
	quit chan struct{}
	done chan struct{}
	once sync.Once
}

func (w *queueWaiter) stop() {
	w.once.Do(func() { close(w.quit) })
	<-w.done
}

// startQueueWait shows a just-enqueued turn's queue position in a draft
// while it waits behind another conversation of the chat (private
// chats with drafts only; a group's queued turn shows 👀). It polls the
// queue every stream tick, re-sends only when the position changed or
// the draft is about to expire, and ends when the turn leaves the
// queue; runAgentStreaming stops it synchronously before its first
// draft (stopQueueWaits), so a stale position never lands after it.
func (b *Bot) startQueueWait(ctx context.Context, item queue.Item) {
	if b.queue == nil || !b.streamEnabled || item.ID == 0 || !b.draftsSupported(item.ChatID) {
		return
	}
	if b.queue.Position(item.Folder, item.ID) == 0 {
		return
	}
	tick := b.streamTick
	if tick <= 0 {
		tick = time.Second
	}
	w := &queueWaiter{quit: make(chan struct{}), done: make(chan struct{})}
	k := chatThread{item.ChatID, item.ThreadID}
	b.statusMu.Lock()
	if b.queueWaits == nil {
		b.queueWaits = make(map[chatThread][]*queueWaiter)
	}
	b.queueWaits[k] = append(b.queueWaits[k], w)
	b.statusMu.Unlock()

	go func() {
		defer close(w.done)
		defer b.forgetQueueWaiter(k, w)
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		draftID := b.newDraftID()
		shown := 0
		var sentAt, hold time.Time
		for {
			pos := b.queue.Position(item.Folder, item.ID)
			if pos == 0 {
				return
			}
			// The same thread streams another turn: its draft is there.
			due := pos != shown || time.Since(sentAt) >= draftRefresh
			if due && !b.threadBusy(item.ChatID, item.ThreadID) && !time.Now().Before(hold) {
				select {
				case <-w.quit:
					return
				default:
				}
				err := b.sendDraftOpts(ctx, item.ChatID, item.ThreadID, draftID, statusHeader(0, 0, pos), draftOpts{})
				switch {
				case err == nil:
					shown, sentAt = pos, time.Now()
				case !b.draftsSupported(item.ChatID) || errors.Is(err, errThreadGone):
					return
				default:
					if wait, ok := retryAfter(err); ok {
						hold = time.Now().Add(wait)
					}
					b.log.Debug("queue draft failed",
						slog.Int64("chat_id", item.ChatID), slog.Any("err", err))
				}
			}
			select {
			case <-w.quit:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (b *Bot) forgetQueueWaiter(k chatThread, w *queueWaiter) {
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	ws := b.queueWaits[k]
	for i, x := range ws {
		if x == w {
			ws = append(ws[:i:i], ws[i+1:]...)
			break
		}
	}
	if len(ws) == 0 {
		delete(b.queueWaits, k)
	} else {
		b.queueWaits[k] = ws
	}
}

// stopQueueWaits stops every queue draft loop of the chat thread (the
// turns of the batch that starts now) and returns once they exited.
func (b *Bot) stopQueueWaits(chatID int64, thread int) {
	b.statusMu.Lock()
	ws := append([]*queueWaiter(nil), b.queueWaits[chatThread{chatID, thread}]...)
	b.statusMu.Unlock()
	for _, w := range ws {
		w.stop()
	}
}
