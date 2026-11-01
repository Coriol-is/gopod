package queue

import (
	"context"
	"errors"
)

var ErrForcedRestart = errors.New("forced restart: cancellation requested")

type activeBatch struct {
	items  []Item
	cancel context.CancelFunc
	forced bool
}

// BeginDrain closes dispatch, not ingress. Accepted input remains durable/pending.
func (q *Queue) BeginDrain()    { q.mu.Lock(); q.draining = true; q.signalDrainLocked(); q.mu.Unlock() }
func (q *Queue) Draining() bool { q.mu.Lock(); defer q.mu.Unlock(); return q.draining }
func (q *Queue) signalDrainLocked() {
	if q.drainWake != nil {
		close(q.drainWake)
	}
	q.drainWake = make(chan struct{})
}
func (q *Queue) beginBatch(ctx context.Context, folder string) ([]Item, context.Context) {
	q.mu.Lock()
	defer q.mu.Unlock()
	items := q.drainPendingLocked(folder)
	if len(items) == 0 {
		return nil, ctx
	}
	runCtx, cancel := context.WithCancel(ctx)
	if q.running == nil {
		q.running = make(map[string]*activeBatch)
	}
	q.running[folder] = &activeBatch{items: items, cancel: cancel}
	q.signalDrainLocked()
	return items, runCtx
}
func (q *Queue) endBatch(folder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if b := q.running[folder]; b != nil {
		b.cancel()
		delete(q.running, folder)
		q.signalDrainLocked()
	}
}
func (q *Queue) batchForced(folder string) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	b := q.running[folder]
	return b != nil && b.forced
}
func (q *Queue) WaitDrained(ctx context.Context) error {
	for {
		q.mu.Lock()
		if len(q.running) == 0 {
			q.mu.Unlock()
			return nil
		}
		if q.drainWake == nil {
			q.drainWake = make(chan struct{})
		}
		wake := q.drainWake
		q.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// CancelDrain resumes pending lanes without duplicating their queue entries.
func (q *Queue) CancelDrain() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.forcing {
		return
	}
	q.draining = false
	for folder, cs := range q.chats {
		if cs.active {
			cs.resumeAfterDrain = true
		}
		if q.closed || cs.active || cs.held > 0 || len(cs.pending) == 0 {
			continue
		}
		cs.active = true
		q.wg.Add(1)
		go func(f string) { defer q.wg.Done(); q.worker(q.ctx, f) }(folder)
	}
	q.signalDrainLocked()
}

// ForceDrain persists cancellation intent for the exact active/pending background
// cutoff BEFORE cancelling handlers. persist must not call back into the queue.
func (q *Queue) ForceDrain(ctx context.Context, persist func([]Item) error) error {
	q.mu.Lock()
	q.draining = true
	var cutoff []Item
	for _, b := range q.running {
		cutoff = append(cutoff, b.items...)
	}
	for _, cs := range q.chats {
		for _, it := range cs.pending {
			if it.Source == "task" || it.Managed() {
				cutoff = append(cutoff, it)
			}
		}
	}
	if err := persist(cutoff); err != nil {
		q.mu.Unlock()
		return err
	}
	q.forcing = true
	var cancelled []Item
	for _, cs := range q.chats {
		keep := cs.pending[:0]
		for _, it := range cs.pending {
			if it.Source == "task" || it.Managed() {
				cancelled = append(cancelled, it)
			} else {
				keep = append(keep, it)
			}
		}
		cs.pending = keep
	}
	for _, b := range q.running {
		b.forced = true
		b.cancel()
	}
	q.signalDrainLocked()
	q.mu.Unlock()
	for _, it := range cancelled {
		if !it.Managed() {
			q.finish([]Item{it}, StatusStopped, ErrForcedRestart)
		}
	}
	return nil
}
