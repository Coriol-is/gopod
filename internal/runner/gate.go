package runner

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Agent admission control (D026, ROADMAP T8).
//
// RAM on the deploy host is spent by running agents, not by idle
// containers, so every start of an agent process (Run, RunStream,
// RunFresh) passes one global gate: at most max agents run at once
// across all chats and threads, and a new one starts only while the
// host's MemAvailable is at least minAvailMB.
//
// Acquire order, everywhere: queue slot (internal/queue's semaphore,
// held by the worker for the whole handler) -> per-chat agent slot
// (acquireAgentSlot, streamed turns only) -> gate. Nothing acquires a
// queue slot or a per-chat slot while holding the gate, and background
// runs (RunFresh: extraction, compaction, task parsing) hold neither a
// queue slot nor a per-chat slot. So a gate holder never waits on
// anything a gate waiter holds, and every wait honours ctx, which the
// queue cancels on shutdown and the Stop button cancels per turn.

// AgentClass is the priority of an agent start.
type AgentClass int

const (
	// ClassInteractive is a user turn (Run, RunStream).
	ClassInteractive AgentClass = iota
	// ClassBackground is a system run (RunFresh). Served after every
	// waiting interactive start; never preempts a running agent.
	ClassBackground
)

func (c AgentClass) String() string {
	if c == ClassBackground {
		return "background"
	}
	return "interactive"
}

// WaitReason says why an agent start has to wait.
type WaitReason int

const (
	// WaitSlot: all agent slots are taken (or earlier callers are queued).
	WaitSlot WaitReason = iota + 1
	// WaitMemory: a slot is free but the host is short of memory.
	WaitMemory
)

func (r WaitReason) String() string {
	if r == WaitMemory {
		return "memory"
	}
	return "slot"
}

// Defaults for the admission settings (internal/config reads the env).
const (
	DefaultMaxAgents       = 2
	DefaultMinAvailableMB  = 900
	DefaultAgentNodeHeapMB = 768
)

const (
	// gateMemPoll is how often a start blocked on memory re-reads it.
	gateMemPoll = 2 * time.Second
	// gateMemMaxWait bounds a memory wait when no agent runs: after it
	// the start proceeds with a Warn, so a mis-sized threshold cannot
	// make the bot silent forever.
	gateMemMaxWait = 60 * time.Second
)

// agentGate is the global admission gate. Waiters queue per class and
// admit themselves: only the head (first interactive waiter, else first
// background waiter) may take a slot, so FIFO and priority hold, and a
// cancelled waiter just leaves the queue; there is no hand-off that
// could give a slot to a caller that is gone.
type agentGate struct {
	max        int
	minAvailMB int64
	readMem    func() (int64, bool) // MemAvailable in MiB; nil or !ok = sufficient
	poll       time.Duration
	memMaxWait time.Duration
	log        *slog.Logger

	mu      sync.Mutex
	running int
	waiting [2][]*gateWaiter // indexed by AgentClass, FIFO
	wake    chan struct{}    // closed and replaced on every state change
}

type gateWaiter struct {
	class AgentClass
	since time.Time
}

// newGate builds a gate admitting max agents (minimum 1). minAvailMB 0
// disables the memory check.
func newGate(max int, minAvailMB int64, readMem func() (int64, bool), log *slog.Logger) *agentGate {
	if max < 1 {
		max = 1
	}
	if log == nil {
		log = slog.Default()
	}
	return &agentGate{
		max:        max,
		minAvailMB: minAvailMB,
		readMem:    readMem,
		poll:       gateMemPoll,
		memMaxWait: gateMemMaxWait,
		log:        log,
		wake:       make(chan struct{}),
	}
}

// acquire blocks until the caller may start an agent of class, or ctx
// ends. onWait (may be nil) is called once, on the caller's goroutine,
// the first time the caller has to wait; it is never called when there
// is capacity. release frees the slot and is idempotent.
func (g *agentGate) acquire(ctx context.Context, class AgentClass, onWait func(WaitReason)) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner: agent gate: %w", err)
	}
	w := &gateWaiter{class: class, since: time.Now()}
	g.mu.Lock()
	g.waiting[class] = append(g.waiting[class], w)
	g.mu.Unlock()

	notified := false
	for {
		reason, wake, ok := g.tryAdmit(w)
		if ok {
			return g.releaser(), nil
		}
		if !notified {
			notified = true
			if onWait != nil {
				onWait(reason)
			}
		}
		var poll <-chan time.Time
		var timer *time.Timer
		if reason == WaitMemory {
			timer = time.NewTimer(g.poll)
			poll = timer.C
		}
		select {
		case <-wake:
		case <-poll:
		case <-ctx.Done():
			if timer != nil {
				timer.Stop()
			}
			g.leave(w)
			return nil, fmt.Errorf("runner: waiting for an agent slot: %w", ctx.Err())
		}
		if timer != nil {
			timer.Stop()
		}
	}
}

// tryAdmit admits w if it is the head and there is room. Otherwise it
// returns why w waits and the channel that signals the next change.
func (g *agentGate) tryAdmit(w *gateWaiter) (WaitReason, <-chan struct{}, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.head() != w || g.running >= g.max {
		return WaitSlot, g.wake, false
	}
	if g.minAvailMB > 0 && g.readMem != nil {
		if avail, ok := g.readMem(); ok && avail < g.minAvailMB {
			waited := time.Since(w.since)
			if g.running > 0 || waited < g.memMaxWait {
				return WaitMemory, g.wake, false
			}
			g.log.Warn("agent gate: memory still low, starting anyway on an idle system",
				slog.String("class", w.class.String()),
				slog.Int64("available_mb", avail),
				slog.Int64("min_available_mb", g.minAvailMB),
				slog.Duration("waited", waited))
		}
	}
	g.removeLocked(w)
	g.running++
	g.broadcastLocked() // the next head may fit too
	return 0, nil, true
}

// head is the waiter allowed to take the next slot. Caller holds mu.
func (g *agentGate) head() *gateWaiter {
	if q := g.waiting[ClassInteractive]; len(q) > 0 {
		return q[0]
	}
	if q := g.waiting[ClassBackground]; len(q) > 0 {
		return q[0]
	}
	return nil
}

// leave removes a waiter that gave up and wakes the others: it may have
// been the head blocking them.
func (g *agentGate) leave(w *gateWaiter) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.removeLocked(w)
	g.broadcastLocked()
}

func (g *agentGate) removeLocked(w *gateWaiter) {
	q := g.waiting[w.class]
	for i, x := range q {
		if x == w {
			g.waiting[w.class] = append(q[:i:i], q[i+1:]...)
			return
		}
	}
}

func (g *agentGate) broadcastLocked() {
	close(g.wake)
	g.wake = make(chan struct{})
}

func (g *agentGate) releaser() func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.running--
			g.broadcastLocked()
		})
	}
}

// waitNoticeKey carries the caller's WaitNotice in a context.
type waitNoticeKey struct{}

// WaitNotice is told, once, that an agent start has to wait for the
// agent gate and why. It runs on the starting goroutine before the wait
// begins, so whatever it shows the user is in place before the agent
// starts; it must not block for long.
type WaitNotice func(WaitReason)

// WithWaitNotice returns a context whose agent starts report a wait to
// fn. The Telegram layer uses it to show "waiting" in the draft or
// placeholder. It travels in ctx so Run/RunStream keep their signatures.
func WithWaitNotice(ctx context.Context, fn WaitNotice) context.Context {
	return context.WithValue(ctx, waitNoticeKey{}, fn)
}

func waitNoticeFrom(ctx context.Context) WaitNotice {
	fn, _ := ctx.Value(waitNoticeKey{}).(WaitNotice)
	return fn
}

// admit passes the agent gate for one agent start of chatFolder. It logs
// at Info when the start has to wait and when it gets in (with the wait
// duration), and forwards the wait to the ctx's WaitNotice. A Runner
// without a gate (unit tests building Runner literals) admits at once.
func (r *Runner) admit(ctx context.Context, chatFolder string, class AgentClass) (func(), error) {
	if r.gate == nil {
		return func() {}, nil
	}
	notice := waitNoticeFrom(ctx)
	var waitStart time.Time
	onWait := func(reason WaitReason) {
		waitStart = time.Now()
		running, waiting := r.gate.counts()
		r.log.Info("agent start waiting",
			slog.String("chat", chatFolder),
			slog.String("class", class.String()),
			slog.String("reason", reason.String()),
			slog.Int("running", running),
			slog.Int("waiting", waiting),
			slog.Int("max_agents", r.gate.max))
		if notice != nil {
			notice(reason)
		}
	}
	release, err := r.gate.acquire(ctx, class, onWait)
	if err != nil {
		if !waitStart.IsZero() {
			r.log.Info("agent start abandoned while waiting",
				slog.String("chat", chatFolder),
				slog.String("class", class.String()),
				slog.Duration("waited", time.Since(waitStart)))
		}
		return nil, err
	}
	if !waitStart.IsZero() {
		r.log.Info("agent start admitted",
			slog.String("chat", chatFolder),
			slog.String("class", class.String()),
			slog.Duration("waited", time.Since(waitStart)))
	}
	return release, nil
}

// counts reports running agents and queued starts, for logs.
func (g *agentGate) counts() (running, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running, len(g.waiting[ClassInteractive]) + len(g.waiting[ClassBackground])
}
