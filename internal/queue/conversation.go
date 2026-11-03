package queue

import (
	"context"
	"errors"
	"fmt"
)

var ErrConversationCancelled = errors.New("queue: conversation cancellation requested")

type conversationKey struct {
	folder string
	thread int
}

// BeginConversationQuiescence closes just this topic's dispatch lane and cancels
// its active handler. A handler returning is not proof of native process exit.
func (q *Queue) BeginConversationQuiescence(folder string, thread int) uint64 {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.conversationBlocks == nil {
		q.conversationBlocks = make(map[conversationKey]uint64)
	}
	q.conversationGeneration++
	generation := q.conversationGeneration
	q.conversationBlocks[conversationKey{folder, thread}] = generation
	if b := q.running[folder]; b != nil && len(b.items) > 0 && b.items[0].ThreadID == thread {
		b.conversationCancelled = true
		b.cancel()
	}
	q.signalDrainLocked()
	return generation
}
func (q *Queue) conversationBlockedLocked(folder string, thread int) bool {
	return q.conversationBlocks[conversationKey{folder, thread}] != 0
}
func (q *Queue) batchConversationCancelled(folder string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.running[folder]
	return b != nil && b.conversationCancelled
}

// cancelConversationPending marks only never-dispatched queue entries stopped.
// It holds the queue lock across checked writes, so no worker or concurrent
// scheduling can take an item between persistence and removal. Failed writes
// retain the item behind the closed lane and return an error to the orchestrator.
func (q *Queue) cancelConversationPending(ctx context.Context, folder string, thread int) error {
	q.mu.Lock()
	cs := q.chats[folder]
	if cs == nil {
		q.mu.Unlock()
		return nil
	}
	keep := cs.pending[:0]
	var settled []Item
	var failure error
	for _, it := range cs.pending {
		if it.ThreadID != thread {
			keep = append(keep, it)
			continue
		}
		if it.Managed() {
			keep = append(keep, it)
			failure = errors.Join(failure, errors.New("managed conversation cancellation requires ledger settlement"))
			continue
		}
		status, reason := StatusStopped, ErrConversationCancelled.Error()
		var noReplay interface{ NoReplay() bool }
		if errors.As(it.cancellationError, &noReplay) && noReplay.NoReplay() {
			status = StatusFailed
			reason = it.cancellationError.Error()
		}
		if q.store != nil && it.ID != 0 {
			if err := q.store.MarkFinished(ctx, it.ID, status, reason); err != nil {
				keep = append(keep, it)
				failure = errors.Join(failure, err)
				continue
			}
		}
		settled = append(settled, it)
	}
	cs.pending = keep
	hooks := append([]func(Item, error){}, q.onDone...)
	q.signalDrainLocked()
	q.mu.Unlock()
	for _, it := range settled {
		for _, hook := range hooks {
			hook(it, ErrConversationCancelled)
		}
	}
	return failure
}

// WaitConversationQuiescent joins the active handler and checked pending-row
// settlement. New entries racing persistence are also caught. It never marks a
// running native process as stopped; the caller must join Runner separately.
func (q *Queue) WaitConversationQuiescent(ctx context.Context, folder string, thread int) error {
	for {
		q.mu.Lock()
		blocked := q.conversationBlockedLocked(folder, thread)
		q.mu.Unlock()
		if !blocked {
			return errors.New("conversation dispatch must be closed before waiting")
		}
		if err := q.cancelConversationPending(ctx, folder, thread); err != nil {
			return fmt.Errorf("settling cancelled queued turns: %w", err)
		}
		q.mu.Lock()
		b := q.running[folder]
		active := b != nil && len(b.items) > 0 && b.items[0].ThreadID == thread
		pending := false
		if cs := q.chats[folder]; cs != nil {
			for _, it := range cs.pending {
				pending = pending || it.ThreadID == thread
			}
		}
		if !active && !pending && q.enqueueing[conversationKey{folder, thread}] == 0 {
			q.mu.Unlock()
			return nil
		}
		wake := q.drainWake
		if wake == nil {
			q.signalDrainLocked()
			wake = q.drainWake
		}
		q.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}
func (q *Queue) ReopenConversation(folder string, thread int, generation uint64) error {
	return q.ReopenConversationAfter(folder, thread, generation, func() error { return nil })
}

// ReopenConversationAfter preserves the established queue -> Store lock order.
// validate runs while dispatch is closed and must not call Queue methods.
func (q *Queue) ReopenConversationAfter(folder string, thread int, generation uint64, validate func() error) error {
	q.mu.Lock()
	if generation == 0 || q.conversationBlocks[conversationKey{folder, thread}] != generation {
		q.mu.Unlock()
		return errors.New("stale conversation fence")
	}
	if b := q.running[folder]; b != nil && len(b.items) > 0 && b.items[0].ThreadID == thread {
		q.mu.Unlock()
		return errors.New("conversation handler still active")
	}
	if err := validate(); err != nil {
		q.mu.Unlock()
		return err
	}
	delete(q.conversationBlocks, conversationKey{folder, thread})
	q.signalDrainLocked()
	q.mu.Unlock()
	q.kick(folder)
	return nil
}

// settleCancelledBatch preserves persistence failures for explicit settlement;
// unlike normal finish it never notifies completion for an uncommitted row.
func (q *Queue) settleCancelledBatch(items []Item, runErr error) {
	items = append([]Item(nil), items...)
	for i := range items {
		items[i].cancellationError = runErr
	}
	q.mu.Lock()
	folder := items[0].Folder
	cs := q.chats[folder]
	if cs == nil {
		cs = &chatState{}
		q.chats[folder] = cs
	}
	cs.pending = append(append([]Item(nil), items...), cs.pending...)
	q.signalDrainLocked()
	q.mu.Unlock()
	// The waiter owns checked settlement after the handler has left running.
}
