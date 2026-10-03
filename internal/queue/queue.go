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
//   - **Durability.** With a TurnStore, every item is a `turns` row before it is dispatched and is marked running/done/failed/interrupted around the handler call. Nil store = memory only.
//
// The queue is transport-agnostic: it takes a [Handler] callback that
// does the actual agent call + Telegram reply. The telegram package
// wires the callback at construction time.
package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Item is one enqueued message waiting for an agent turn.
type Item struct {
	// Durability fields. Zero when the queue has no TurnStore.
	ID               int64  // turns.id
	Source           string // "telegram" | "task"
	SourceID         string // tg message id | "<task_id>:<next_run>"
	Resumed          bool   // set by Recover: this turn was running/interrupted when gopod died
	Attempts         int    // from the row, for the crash-loop cap
	PlaceholderMsgID int    // streaming placeholder recorded by a previous attempt
	ReplyMsgID       int    // final reply recorded by a previous attempt

	ChatID    int64  // Telegram chat ID (for the reply callback), 0 for tasks
	MessageID int    // Telegram message ID (for reply threading + reactions)
	Folder    string // registered chat folder
	IsOwner   bool
	Text      string // the user's message text
	FilePath  string // container-side path to an uploaded file (photo/doc), empty if text-only
	IsVoice   bool   // true if the original message was a voice message
}

// Turn statuses written through TurnStore. Mirror store.Turn* so the
// queue does not import store.
const (
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusInterrupted = "interrupted"
)

// MaxAttempts is how many times a turn may be started (across process
// restarts) before Recover declares it a crash loop.
const MaxAttempts = 3

// TurnStore persists turns. Declared here, at the consumer, so the
// store package stays free of queue imports; cmd/gopod adapts
// *store.Store to it.
type TurnStore interface {
	InsertTurn(ctx context.Context, it Item) (id int64, dup bool, err error)
	MarkRunning(ctx context.Context, id int64) error
	MarkFinished(ctx context.Context, id int64, status, errText string) error
	LoadUnfinished(ctx context.Context) ([]Item, error)
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
	store      TurnStore // nil = memory only

	// BackoffFunc computes the wait duration for a given retry count.
	// Defaults to the exponential backoffDuration. Tests override this
	// to avoid real sleeps.
	BackoffFunc func(retry int) time.Duration

	ctx    context.Context // workers derive from this; cancelled by Close
	cancel context.CancelFunc
	wg     sync.WaitGroup

	mu     sync.Mutex
	chats  map[string]*chatState // keyed by folder
	onDone []func(Item, error)
	closed bool // set by Close; schedule refuses new work afterwards
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

// New creates a Queue. Workers derive their context from ctx (the app
// context), so cancelling it interrupts in-flight turns; see Close.
// maxConcurrent ≤ 0 defaults to DefaultMaxConcurrent. handler must not
// be nil. store may be nil (memory-only, dev mode).
func New(ctx context.Context, handler Handler, maxConcurrent int, store TurnStore, log *slog.Logger) *Queue {
	if maxConcurrent <= 0 {
		maxConcurrent = DefaultMaxConcurrent
	}
	if log == nil {
		log = slog.Default()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	wctx, cancel := context.WithCancel(ctx)
	return &Queue{
		handler:     handler,
		maxConc:     maxConcurrent,
		sem:         make(chan struct{}, maxConcurrent),
		log:         log,
		maxRetries:  DefaultMaxRetries,
		store:       store,
		BackoffFunc: backoffDuration,
		ctx:         wctx,
		cancel:      cancel,
		chats:       make(map[string]*chatState),
	}
}

// OnDone registers a hook called after a turn reaches a terminal
// status (done, failed, interrupted) or is rejected by Recover as a
// crash loop. Hooks run synchronously in the worker goroutine, in
// registration order. err is the last handler error, nil on success.
func (q *Queue) OnDone(fn func(Item, error)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onDone = append(q.onDone, fn)
}

// Close cancels every worker's context and waits for them to exit.
// In-flight turns are marked interrupted. Safe to call once.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	q.mu.Unlock()
	q.cancel()
	q.wg.Wait()
}

// Enqueue persists the item (when a TurnStore is configured) and
// schedules it for the chat. Returns the turn id and whether the item
// was a duplicate of an existing turn (same Source+SourceID), in which
// case nothing was scheduled. Without a store both are zero.
//
// If the chat has no active worker, one is started (subject to the
// global concurrency cap). If the chat already has an active worker,
// the item is appended to the pending queue.
//
// Enqueue never blocks on the global cap. The worker runs on the
// queue's own context (derived from the ctx passed to New), NOT the
// caller's ctx, which for Telegram is request-scoped and cancelled
// when the handler returns.
func (q *Queue) Enqueue(ctx context.Context, item Item) (int64, bool) {
	if q.store != nil {
		id, dup, err := q.store.InsertTurn(ctx, item)
		if err != nil {
			q.log.Error("queue: persist turn failed, dropping",
				slog.String("folder", item.Folder),
				slog.String("source", item.Source),
				slog.String("source_id", item.SourceID),
				slog.Any("err", err))
			return 0, false
		}
		if dup {
			q.log.Debug("queue: duplicate turn ignored",
				slog.String("source", item.Source),
				slog.String("source_id", item.SourceID))
			return 0, true
		}
		item.ID = id
	}
	q.schedule(item)
	return item.ID, false
}

// schedule appends item to its chat's pending list and starts a worker
// if none is active. Shared by Enqueue and Recover.
func (q *Queue) schedule(item Item) {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		q.log.Debug("queue closed, turn left pending for recovery",
			slog.String("folder", item.Folder),
			slog.Int64("turn", item.ID))
		return
	}
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
	// Add under the same lock hold as the closed check so Close wg.Wait
	// cannot see a zero counter before this worker is counted.
	q.wg.Add(1)
	q.mu.Unlock()

	go func() {
		defer q.wg.Done()
		q.worker(q.ctx, item.Folder)
	}()
}

// ErrCrashLoop is passed to OnDone hooks for a turn that Recover
// refused to replay because it already reached MaxAttempts.
var ErrCrashLoop = errors.New("queue: turn exceeded max attempts across restarts")

// Recover loads every unfinished turn from the store and schedules it.
// Rows that were running or interrupted when the previous process died
// are re-enqueued with Resumed=true so the handler can tell the agent
// its last turn was cut off. Rows that already reached MaxAttempts are
// marked failed and reported through OnDone instead of being replayed.
//
// Call once at boot, after producers and hooks are wired and before
// Telegram starts polling. Returns the number of turns scheduled.
func (q *Queue) Recover(ctx context.Context) (int, error) {
	if q.store == nil {
		return 0, nil
	}
	items, err := q.store.LoadUnfinished(ctx)
	if err != nil {
		return 0, fmt.Errorf("queue: load unfinished turns: %w", err)
	}
	n := 0
	for _, it := range items {
		if it.Attempts >= MaxAttempts {
			q.log.Error("queue: turn exceeded max attempts, giving up",
				slog.Int64("turn", it.ID),
				slog.String("folder", it.Folder),
				slog.Int("attempts", it.Attempts))
			q.finish([]Item{it}, StatusFailed, ErrCrashLoop)
			continue
		}
		it.Resumed = it.Attempts > 0 // running/interrupted rows were started at least once
		q.log.Info("queue: recovering turn",
			slog.Int64("turn", it.ID),
			slog.String("folder", it.Folder),
			slog.Bool("resumed", it.Resumed))
		q.schedule(it)
		n++
	}
	return n, nil
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
		// select picks randomly when both cases are ready, so re-check.
		if ctx.Err() != nil {
			q.deactivate(folder)
			return
		}
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
			q.markRunning(ctx, items)
			err := q.handler(ctx, items)
			if err == nil {
				q.resetRetries(folder)
				q.finish(items, StatusDone, nil)
				break // success → drain more pending in outer loop
			}
			if ctx.Err() != nil {
				// Shutdown, not a failure: leave the rows for Recover.
				q.log.Info("queue: turn interrupted by shutdown",
					slog.String("folder", folder))
				q.finish(items, StatusInterrupted, err)
				q.deactivate(folder)
				return
			}

			retries := q.incrementRetries(folder)
			if retries > q.maxRetries {
				q.log.Error("queue: max retries exceeded, batch failed; remaining pending items kept",
					slog.String("folder", folder),
					slog.Int("retries", retries),
					slog.Any("err", err))
				q.finish(items, StatusFailed, err)
				q.deactivate(folder)
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
				q.finish(items, StatusInterrupted, ctx.Err())
				q.deactivate(folder)
				return
			}
		}
	}
}

// markRunning flips every item in the batch to running. Store errors
// are logged, not fatal: the turn still runs, and Recover may replay it
// once more than necessary, which the reply memo tolerates.
func (q *Queue) markRunning(ctx context.Context, items []Item) {
	if q.store == nil {
		return
	}
	for _, it := range items {
		if it.ID == 0 {
			continue
		}
		if err := q.store.MarkRunning(context.WithoutCancel(ctx), it.ID); err != nil {
			q.log.Error("queue: mark running", slog.Int64("turn", it.ID), slog.Any("err", err))
		}
	}
}

// finish marks every item in the batch with status and fires the
// OnDone hooks. Uses a context detached from cancellation so shutdown
// bookkeeping still reaches the store.
func (q *Queue) finish(items []Item, status string, err error) {
	errText := ""
	if err != nil {
		errText = err.Error()
	}
	if q.store != nil {
		for _, it := range items {
			if it.ID == 0 {
				continue
			}
			if merr := q.store.MarkFinished(context.WithoutCancel(q.ctx), it.ID, status, errText); merr != nil {
				q.log.Error("queue: mark finished", slog.Int64("turn", it.ID), slog.Any("err", merr))
			}
		}
	}
	q.mu.Lock()
	hooks := append([]func(Item, error){}, q.onDone...)
	q.mu.Unlock()
	for _, it := range items {
		for _, h := range hooks {
			h(it, err)
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
