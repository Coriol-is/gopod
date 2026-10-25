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
// logged, never retried by the queue.

import (
	"context"
	"time"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
)

// lastReplyMax bounds the conversations whose newest reply is
// remembered; the oldest entry is dropped beyond it.
const lastReplyMax = 256

// replyRow is the in-memory state of the reply action rows.
type replyRow struct {
	// last is the newest reply carrying the row, per conversation.
	last map[chatThread]lastReply
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

// noteReply runs after recordReply for every final reply of a turn:
// msgID (0 = nothing delivered, no-op) is now the newest reply of the
// turn's conversation. withRow says it carries the action row; either
// way the previous reply's row in that conversation is removed.
func (b *Bot) noteReply(ctx context.Context, item queue.Item, msgID int, withRow bool) {
	if msgID == 0 || item.ChatID == 0 {
		return
	}
	key := chatThread{item.ChatID, item.ThreadID}
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
		b.clearKeyboard(context.WithoutCancel(ctx), item.ChatID, prev.msgID)
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
