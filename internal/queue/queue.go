// Package queue implements gopod's per-chat serialization and global
// concurrency cap for agent runs.
//
// The design mirrors NanoClaw's `src/group-queue.ts`. Key properties:
//
//   - **Per-chat serialization.** At most one agent run is in flight per
//     chat. Messages that arrive while the agent is busy are queued; when
//     the current run finishes, a new run starts for any pending messages
//     (coalesced into one turn, not one-per-message).
//   - **Global concurrency cap.** At most N chats can have a concurrent
//     agent run. The cap is enforced by a buffered channel acting as a
//     counting semaphore. Chats that exceed the cap wait until a slot
//     frees up.
//   - **Exponential backoff.** On failure the chat backs off before
//     retrying (5s → 10s → 20s → 40s → 80s, max 5 retries). Success
//     resets the retry counter.
//
// The queue is transport-agnostic: it takes a [Handler] callback that
// does the actual agent call + Telegram reply. The telegram package
// wires the callback at construction time.
package queue

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// Item is one enqueued message waiting for an agent turn.
type Item struct {
	ChatID    int64  // Telegram chat ID (for the reply callback)
	MessageID int    // Telegram message ID (for reply threading + reactions)
	Folder    string // registered chat folder
	IsOwner   bool
	Text      string // the user's message text
	FilePath  string // container-side path to an uploaded file (photo/doc), empty if text-only
	IsVoice   bool   // true if the original message was a voice message
}

// Handler is the callback the queue invokes for each agent turn.
// It receives the item(s) pending for a chat and is responsible for
// running the agent, sending the Telegram reply, and returning any
// error. The queue uses the error to decide whether to retry with
// backoff.
//
// items always has at least one element. When multiple messages
// arrived while the agent was busy, items contains all of them in
// arrival order — the handler can choose to pass only the last one
// to claude (cheapest) or to format a multi-message prompt.
type Handler func(ctx context.Context, items []Item) error

// Queue is gopod's agent-run scheduler.
type Queue struct {
	handler    Handler
	maxConc    int
	sem        chan struct{} // buffered channel, size = maxConc
	log        *slog.Logger
	maxRetries int

	// BackoffFunc computes the wait duration for a given retry count.
	// Defaults to the exponential backoffDuration. Tests override this
	// to avoid real sleeps.
	BackoffFunc func(retry int) time.Duration

	mu    sync.Mutex
	chats map[string]*chatState // keyed by folder
}

type chatState struct {
	pending []Item
	active  bool
	retries int
}

// DefaultMaxConcurrent is the default global cap on simultaneous
// agent runs across all chats.
const DefaultMaxConcurrent = 3

// DefaultMaxRetries is how many times the queue retries a failing
// chat before dropping the pending items.
const DefaultMaxRetries = 5

// New creates a Queue. maxConcurrent ≤ 0 defaults to
// DefaultMaxConcurrent. handler must not be nil.
func New(handler Handler, maxConcurrent int, log *slog.Logger) *Queue {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	if log == nil {
		log = slog.Default()
	}
	return &Queue{
		handler:     handler,
		maxConc:     maxConcurrent,
		sem:         make(chan struct{}, maxConcurrent),
		log:         log,
		maxRetries:  DefaultMaxRetries,
		BackoffFunc: backoffDuration,
		chats:       make(map[string]*chatState),
	}
}

// Enqueue adds an item for the chat. If the chat has no active worker,
// one is started (subject to the global concurrency cap). If the chat
// already has an active worker, the item is appended to the pending
// queue — the worker will pick it up after the current run finishes.
//
// Enqueue never blocks on the global cap: if all slots are taken, a
// background goroutine waits for a slot and then starts the worker.
// The caller (Telegram default handler) returns immediately in all
// cases.
//
// IMPORTANT: the worker uses context.Background(), NOT the caller's
// ctx. The caller's ctx is a request-scoped context from go-telegram/bot
// that may be cancelled when the handler returns. The worker must
// outlive the handler — it runs claude for 10-30 seconds and sends
// the reply asynchronously.
func (q *Queue) Enqueue(ctx context.Context, item Item) {
	q.mu.Lock()
	cs, ok := q.chats[item.Folder]
	if !ok {
		cs = &chatState{}
		q.chats[item.Folder] = cs
	}
	cs.pending = append(cs.pending, item)
	if cs.active {
		q.mu.Unlock()
		return // worker already running, it will drain pending
	}
	cs.active = true
	q.mu.Unlock()

	// Worker uses Background context so it outlives the HTTP handler.
	go q.worker(context.Background(), item.Folder)
}

// Pending returns the number of items waiting across all chats. For
// diagnostics / future /stats command.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, cs := range q.chats {
		n += len(cs.pending)
	}
	return n
}

// ActiveChats returns the number of chats that currently have a
// worker running.
func (q *Queue) ActiveChats() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, cs := range q.chats {
		if cs.active {
			n++
		}
	}
	return n
}

func (q *Queue) worker(ctx context.Context, folder string) {
	// Acquire semaphore slot (blocks if cap reached).
	select {
	case q.sem <- struct{}{}:
	case <-ctx.Done():
		q.deactivate(folder)
		return
	}
	defer func() { <-q.sem }()

	for {
		items := q.drainPending(folder)
		if len(items) == 0 {
			q.deactivate(folder)
			return
		}

		q.log.Debug("queue: running agent turn",
			slog.String("folder", folder),
			slog.Int("items", len(items)))

		// Inner retry loop: retries the SAME items on failure.
		// New messages arriving during backoff append to pending
		// and get picked up in the NEXT outer-loop iteration.
		for {
			err := q.handler(ctx, items)
			if err == nil {
				q.resetRetries(folder)
				break // success → drain more pending in outer loop
			}

			retries := q.incrementRetries(folder)
			if retries > q.maxRetries {
				q.log.Error("queue: max retries exceeded, dropping items",
					slog.String("folder", folder),
					slog.Int("retries", retries),
					slog.Any("err", err))
				q.dropPendingAndDeactivate(folder)
				return
			}
			backoff := q.BackoffFunc(retries)
			q.log.Warn("queue: agent run failed, backing off",
				slog.String("folder", folder),
				slog.Int("retry", retries),
				slog.Duration("backoff", backoff),
				slog.Any("err", err))
			select {
			case <-time.After(backoff):
				// retry same items
			case <-ctx.Done():
				q.deactivate(folder)
				return
			}
		}
	}
}

// drainPending atomically takes all pending items for the chat,
// leaving the pending slice empty. The worker calls this at the
// start of each loop iteration to pick up any messages that arrived
// while the previous run was in flight.
func (q *Queue) drainPending(folder string) []Item {
	q.mu.Lock()
	defer q.mu.Unlock()
	cs := q.chats[folder]
	if cs == nil || len(cs.pending) == 0 {
		return nil
	}
	items := cs.pending
	cs.pending = nil
	return items
}

func (q *Queue) deactivate(folder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if cs, ok := q.chats[folder]; ok {
		cs.active = false
		cs.retries = 0
	}
}

func (q *Queue) dropPendingAndDeactivate(folder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if cs, ok := q.chats[folder]; ok {
		cs.pending = nil
		cs.active = false
		cs.retries = 0
	}
}

func (q *Queue) incrementRetries(folder string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	cs := q.chats[folder]
	if cs == nil {
		return 0
	}
	cs.retries++
	return cs.retries
}

func (q *Queue) resetRetries(folder string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if cs, ok := q.chats[folder]; ok {
		cs.retries = 0
	}
}

// backoffDuration returns the wait time for the given retry count.
// 5s, 10s, 20s, 40s, 80s (exponential, capped).
func backoffDuration(retry int) time.Duration {
	d := 5 * time.Second
	for i := 1; i < retry; i++ {
		d *= 2
		if d > 80*time.Second {
			return 80 * time.Second
		}
	}
	return d
}
