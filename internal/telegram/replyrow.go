package telegram

// The action row under the newest reply (spec F3, owner Q1). The final
// reply of a turn carries replyKeyboard on its last text message (never
// on a document, an error, a stopped or a wait message). Only the
// newest reply of a conversation (chat, thread) keeps its row: when a
// new final reply goes out, the previous one's row is removed with
// editMessageReplyMarkup.
//
// The record of which message holds the row is in memory only and is
// not part of the durable turn (D020): after a restart the next reply
// cannot clear the previous row, which then stays (its buttons still
// work: Regenerate loads the turn from the store). A failed clear is
// logged, never retried by the queue. A reply redirected to main from a
// gone topic has no row and counts as main's newest reply.

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
)

// lastReplyMax bounds the conversations whose newest reply is
// remembered; the oldest entry is dropped beyond it.
const lastReplyMax = 256

// Per-turn memo bounds: the step log keeps a turn's last stepLogMax
// tool steps; a memo lives memoTTL after its last write; at most
// memoMax turns are kept (the oldest goes first).
const (
	stepLogMax = 50
	memoTTL    = time.Hour
	memoMax    = 32
)

// replyRow is the in-memory state of the reply action rows.
type replyRow struct {
	// last is the newest reply carrying the row, per conversation.
	last map[chatThread]lastReply
	// memo keeps what Steps and As file show, per turn id.
	memo map[int64]*turnMemo
	// regenerated is when each turn was last regenerated (double tap).
	regenerated map[int64]time.Time
}

// turnMemo is what a turn's More actions need, kept in memory only: a
// restart or memoTTL loses it, and the buttons then say so.
type turnMemo struct {
	chatID int64
	steps  []string // D024 progress lines, already redacted
	reply  string   // the final reply text as the agent wrote it
	at     time.Time
}

// lastReply is a message that carries the action row.
type lastReply struct {
	msgID int
	at    time.Time
}

// replyMarkup is the action row for a turn's final reply text, nil when
// the turn has no durable id to name (memory-only queue) or is not a
// chat turn.
func (b *Bot) replyMarkup(item queue.Item, text string) models.ReplyMarkup {
	if item.ID == 0 || item.ChatID == 0 || item.Source == "task" {
		return nil
	}
	kb := replyKeyboard(item.ID, text)
	return &kb
}

// noteReply runs after recordReply for a final reply sent with the
// action row: msgID (0 = nothing delivered, no-op) is now the newest
// reply of the turn's conversation, its text is kept for As file, and
// the previous reply's row in that conversation is removed. A reply
// whose topic is gone went to main without the row (sendRendered): it
// is recorded as noteBareReply records it.
func (b *Bot) noteReply(ctx context.Context, item queue.Item, msgID int, text string) {
	if msgID != 0 && item.ID != 0 {
		b.rememberTurn(item, func(m *turnMemo) { m.reply = text })
	}
	if item.ThreadID != 0 && b.threadGone(item.ChatID, item.ThreadID) {
		b.newestReply(ctx, item.ChatID, 0, msgID, false)
		return
	}
	b.newestReply(ctx, item.ChatID, item.ThreadID, msgID, true)
}

// noteBareReply is noteReply for a final reply without the row (stopped):
// it is the newest reply all the same, so the previous row goes. A reply
// redirected from a gone topic is in main, so it is main's newest reply.
func (b *Bot) noteBareReply(ctx context.Context, item queue.Item, msgID int) {
	thread := item.ThreadID
	if thread != 0 && b.threadGone(item.ChatID, thread) {
		thread = 0
	}
	b.newestReply(ctx, item.ChatID, thread, msgID, false)
}

// newestReply records msgID as the newest reply of (chat, thread) and
// clears the previous reply's row there.
func (b *Bot) newestReply(ctx context.Context, chatID int64, thread int, msgID int, withRow bool) {
	if msgID == 0 || chatID == 0 {
		return
	}
	key := chatThread{chatID, thread}
	now := b.clock()
	b.rowMu.Lock()
	prev := b.rows.last[key]
	if withRow {
		if b.rows.last == nil {
			b.rows.last = make(map[chatThread]lastReply)
		}
		b.rows.last[key] = lastReply{msgID: msgID, at: now}
		if len(b.rows.last) > lastReplyMax {
			b.rows.dropOldest()
		}
	} else {
		delete(b.rows.last, key)
	}
	b.rowMu.Unlock()
	if prev.msgID != 0 && prev.msgID != msgID {
		b.clearKeyboard(context.WithoutCancel(ctx), chatID, prev.msgID)
	}
}

// forgetReply drops msgID as the newest reply of (chat, thread) when it
// is: its row was removed by other means (a pressed Regenerate).
func (b *Bot) forgetReply(chatID int64, thread int, msgID int) {
	key := chatThread{chatID, thread}
	b.rowMu.Lock()
	defer b.rowMu.Unlock()
	if b.rows.last[key].msgID == msgID {
		delete(b.rows.last, key)
	}
}

// logStep appends one D024 progress line (progressLine, redacted like
// the draft) to the turn's step log.
func (b *Bot) logStep(item queue.Item, line string) {
	if item.ID == 0 {
		return
	}
	b.rememberTurn(item, func(m *turnMemo) {
		m.steps = append(m.steps, line)
		if len(m.steps) > stepLogMax {
			m.steps = append([]string(nil), m.steps[len(m.steps)-stepLogMax:]...)
		}
	})
}

// rememberTurn applies set to the turn's memo, creating it, and prunes
// expired and excess memos.
func (b *Bot) rememberTurn(item queue.Item, set func(*turnMemo)) {
	now := b.clock()
	b.rowMu.Lock()
	defer b.rowMu.Unlock()
	if b.rows.memo == nil {
		b.rows.memo = make(map[int64]*turnMemo)
	}
	m := b.rows.memo[item.ID]
	if m == nil || now.Sub(m.at) >= memoTTL {
		m = &turnMemo{chatID: item.ChatID}
		b.rows.memo[item.ID] = m
	}
	set(m)
	m.at = now
	var oldest int64
	for id, x := range b.rows.memo {
		if now.Sub(x.at) >= memoTTL {
			delete(b.rows.memo, id)
		} else if id != item.ID && (oldest == 0 || x.at.Before(b.rows.memo[oldest].at)) {
			oldest = id
		}
	}
	if len(b.rows.memo) > memoMax {
		delete(b.rows.memo, oldest)
	}
}

// turnMemoFor returns a copy of the turn's memo when it is still kept
// and belongs to chatID.
func (b *Bot) turnMemoFor(turnID, chatID int64) (turnMemo, bool) {
	b.rowMu.Lock()
	defer b.rowMu.Unlock()
	m := b.rows.memo[turnID]
	if m == nil || m.chatID != chatID || b.clock().Sub(m.at) >= memoTTL {
		return turnMemo{}, false
	}
	c := *m
	c.steps = append([]string(nil), m.steps...)
	return c, true
}

// dropOldest removes the least recent conversation entry.
func (r *replyRow) dropOldest() {
	var oldest chatThread
	var at time.Time
	first := true
	for k, v := range r.last {
		if first || v.at.Before(at) {
			oldest, at, first = k, v.at, false
		}
	}
	delete(r.last, oldest)
}
