package telegram

// Turn state as a reaction on the user's message (spec F5, owner Q3):
//
//	👀 queued → 🤔 agent started → 👨‍💻 tool running → 👍 done
//	                                              → 😱 error
//	                                              → 👌 stopped
//	🥱 no progress for 25 s, 😨 for 60 s, ✍ during /compact
//
// setMessageReaction replaces the message's whole reaction set, so one
// call per state change is enough. Changes are debounced (one write per
// reactDebounce per message) so a burst of tool steps costs one call; a
// final state is written at once and always, even inside a debounce
// window. Reactions are UX sugar: a failed write is logged at debug and
// never fails a turn, and a message the bot cannot react to (no rights
// in a group, deleted message or topic) silently stops getting writes.

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
)

type turnState int

const (
	stNone turnState = iota
	stQueued
	stThinking
	stTool
	stDone
	stError
	stStopped
	stStallSoft
	stStallHard
	stCompacting
)

// stateEmoji maps a state to its reaction. Every value must be in
// Telegram's ReactionTypeEmoji list (TestStatusEmojiAllAllowed): ✅ and
// ❌, for one, are not.
var stateEmoji = map[turnState]string{
	stQueued:     "👀",
	stThinking:   "🤔",
	stTool:       "👨‍💻",
	stDone:       "👍",
	stError:      "😱",
	stStopped:    "👌",
	stStallSoft:  "🥱",
	stStallHard:  "😨",
	stCompacting: "✍",
}

// final reports whether s ends the message's reaction life.
func (s turnState) final() bool {
	return s == stDone || s == stError || s == stStopped
}

const (
	reactDebounce  = 700 * time.Millisecond
	stallSoftAfter = 25 * time.Second
	stallHardAfter = 60 * time.Second
	// reactFinalRetryMax bounds the wait before re-sending a final state
	// refused with 429.
	reactFinalRetryMax = 10 * time.Second
)

// reactClock is the reactor's time source; tests use a fake one.
type reactClock interface {
	Now() time.Time
	AfterFunc(d time.Duration, f func()) (stop func() bool)
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) AfterFunc(d time.Duration, f func()) func() bool {
	return time.AfterFunc(d, f).Stop
}

// statusReactor is one message's reaction state machine. A nil reactor
// (no message to react to) ignores every call.
type statusReactor struct {
	clock reactClock
	// write sets emoji as the message's only reaction.
	write func(emoji string) error
	log   *slog.Logger
	// release forgets the reactor once its final state is written.
	release func()

	writeMu sync.Mutex // serializes writes, so they land in order

	mu        sync.Mutex
	want      turnState // the state to show
	shown     turnState // the state last written
	active    turnState // the last non-stall state (stall recovery)
	lastWrite time.Time
	pending   func() bool // the debounce timer, nil when none
	stall     func() bool // the stall timer, nil when none
	done      bool        // a final state was set: later Sets are ignored
	dead      bool        // the message refuses reactions: no more writes
}

// statusKey names the message a reactor belongs to.
type statusKey struct {
	chat int64
	msg  int
}

// newStatusReactor returns the reactor of a message, creating it on
// first use, so the handler's queued state and the turn's later states
// share one debounce window. nil when msgID is 0 (nothing to react to).
func (b *Bot) newStatusReactor(chatID int64, msgID int) *statusReactor {
	if msgID <= 0 || b.api == nil {
		return nil
	}
	k := statusKey{chatID, msgID}
	b.statusMu.Lock()
	defer b.statusMu.Unlock()
	if r := b.statuses[k]; r != nil {
		return r
	}
	if b.statuses == nil {
		b.statuses = make(map[statusKey]*statusReactor)
	}
	clock := b.reactClock
	if clock == nil {
		clock = realClock{}
	}
	r := &statusReactor{
		clock: clock,
		log:   b.log,
		write: func(emoji string) error {
			// Not the turn's context: a final state must land even when
			// the turn's context ends right after it.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			return b.setReaction(ctx, chatID, msgID, emoji)
		},
	}
	r.release = func() {
		b.statusMu.Lock()
		if b.statuses[k] == r {
			delete(b.statuses, k)
		}
		b.statusMu.Unlock()
	}
	b.statuses[k] = r
	return r
}

// turnStatus is the reactor of the turn's user message.
func (b *Bot) turnStatus(item queue.Item) *statusReactor {
	return b.newStatusReactor(item.ChatID, item.MessageID)
}

// setReaction sets emoji as the message's whole reaction set.
func (b *Bot) setReaction(ctx context.Context, chatID int64, msgID int, emoji string) error {
	_, err := b.api.SetMessageReaction(ctx, &bot.SetMessageReactionParams{
		ChatID:    chatID,
		MessageID: msgID,
		Reaction: []models.ReactionType{{
			Type:              models.ReactionTypeTypeEmoji,
			ReactionTypeEmoji: &models.ReactionTypeEmoji{Emoji: emoji},
		}},
	})
	return err
}

// Set moves the message to state s. A final state is written now; any
// other state at most once per reactDebounce (the latest wins). Thinking
// and tool states count as progress (they reset the stall timer).
func (r *statusReactor) Set(s turnState) {
	if r == nil || s == stNone {
		return
	}
	r.mu.Lock()
	if r.done {
		r.mu.Unlock()
		return
	}
	r.want = s
	if s != stStallSoft && s != stStallHard {
		r.active = s
	}
	if s.final() {
		r.done = true
		r.stopTimersLocked()
		r.mu.Unlock()
		r.flush()
		return
	}
	if s == stThinking || s == stTool {
		r.armStallLocked(stallSoftAfter, stStallSoft)
	}
	r.scheduleLocked()
	r.mu.Unlock()
}

// Touch records progress without a state change: it resets the stall
// timer and brings a stalled message back to its last active state.
func (r *statusReactor) Touch() {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.done || r.stall == nil && r.want != stStallSoft && r.want != stStallHard {
		return // not running (queued, compacting): no stall to reset
	}
	if r.want == stStallSoft || r.want == stStallHard {
		r.want = r.active
		r.scheduleLocked()
	}
	r.armStallLocked(stallSoftAfter, stStallSoft)
}

// scheduleLocked arranges a write of r.want: at once (on the clock's
// goroutine) when the last write is older than reactDebounce, else at
// the end of the window. One timer at a time; it writes the latest want.
func (r *statusReactor) scheduleLocked() {
	if r.pending != nil {
		return
	}
	delay := reactDebounce - r.clock.Now().Sub(r.lastWrite)
	if r.lastWrite.IsZero() || delay < 0 {
		delay = 0
	}
	r.pending = r.clock.AfterFunc(delay, func() {
		r.mu.Lock()
		r.pending = nil
		r.mu.Unlock()
		r.flush()
	})
}

// armStallLocked (re)starts the stall timer: after d without progress
// the message shows s; the soft stall then arms the hard one.
func (r *statusReactor) armStallLocked(d time.Duration, s turnState) {
	if r.stall != nil {
		r.stall()
	}
	r.stall = r.clock.AfterFunc(d, func() {
		r.mu.Lock()
		if r.done {
			r.mu.Unlock()
			return
		}
		r.want = s
		if s == stStallSoft {
			r.armStallLocked(stallHardAfter-stallSoftAfter, stStallHard)
		} else {
			r.stall = nil
		}
		r.scheduleLocked()
		r.mu.Unlock()
	})
}

func (r *statusReactor) stopTimersLocked() {
	if r.pending != nil {
		r.pending()
		r.pending = nil
	}
	if r.stall != nil {
		r.stall()
		r.stall = nil
	}
}

// flush writes r.want when it differs from what the message shows.
func (r *statusReactor) flush() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	s := r.want
	if r.dead || s == r.shown {
		final := r.done
		r.mu.Unlock()
		if final {
			r.release()
		}
		return
	}
	r.shown = s
	r.lastWrite = r.clock.Now()
	final := r.done
	r.mu.Unlock()

	err := r.write(stateEmoji[s])
	if err == nil {
		if final {
			r.release()
		}
		return
	}
	r.log.Debug("status reaction failed", slog.String("emoji", stateEmoji[s]), slog.Any("err", err))
	r.mu.Lock()
	defer r.mu.Unlock()
	r.shown = stNone // unknown: a later state is written again
	if wait, ok := retryAfter(err); ok {
		if final && wait <= reactFinalRetryMax {
			// The final state must land: try once more after the wait.
			r.clock.AfterFunc(wait, func() {
				r.mu.Lock()
				r.done = true
				r.mu.Unlock()
				r.flushOnce()
			})
			return
		}
		if !final {
			// The latest state, once the wait is over.
			r.lastWrite = r.clock.Now().Add(wait - reactDebounce)
			r.scheduleLocked()
		}
		return
	}
	if reactionRefused(err) {
		// No rights in a group, reactions off, message or topic gone:
		// stop writing to this message for good.
		r.dead = true
	}
	if final {
		r.release()
	}
}

// flushOnce is the final state's single 429 retry.
func (r *statusReactor) flushOnce() {
	r.writeMu.Lock()
	defer r.writeMu.Unlock()
	r.mu.Lock()
	s, dead := r.want, r.dead
	r.mu.Unlock()
	if !dead {
		if err := r.write(stateEmoji[s]); err != nil {
			r.log.Debug("status reaction retry failed", slog.Any("err", err))
		}
	}
	r.release()
}

// reactionRefused reports a 400/403 for which retrying the same message
// is pointless.
func reactionRefused(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, s := range []string{"bad request", "forbidden", "not enough rights", "reaction_invalid", "message to react not found", "message_id_invalid", "chat not found"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
