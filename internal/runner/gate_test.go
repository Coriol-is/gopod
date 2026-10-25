package runner

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// pendingAcquire is one acquire running in its own goroutine.
type pendingAcquire struct {
	waits   chan WaitReason // every onWait call
	done    chan struct{}   // closed when acquire returned
	release func()
	err     error
}

func acquireAsync(g *agentGate, ctx context.Context, class AgentClass) *pendingAcquire {
	p := &pendingAcquire{waits: make(chan WaitReason, 16), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		p.release, p.err = g.acquire(ctx, class, func(r WaitReason) { p.waits <- r })
	}()
	return p
}

// waiting blocks until the acquire reported it has to wait.
func (p *pendingAcquire) waiting(t *testing.T) WaitReason {
	t.Helper()
	select {
	case r := <-p.waits:
		return r
	case <-p.done:
		t.Fatalf("acquire returned instead of waiting (err=%v)", p.err)
	case <-time.After(2 * time.Second):
		t.Fatal("acquire neither waited nor returned")
	}
	return 0
}

func (p *pendingAcquire) admitted(t *testing.T) func() {
	t.Helper()
	select {
	case <-p.done:
	case <-time.After(2 * time.Second):
		t.Fatal("acquire not admitted")
	}
	if p.err != nil {
		t.Fatalf("acquire: %v", p.err)
	}
	return p.release
}

func (p *pendingAcquire) stillWaiting(t *testing.T) {
	t.Helper()
	select {
	case <-p.done:
		t.Fatalf("acquire returned while it should wait (err=%v)", p.err)
	case <-time.After(50 * time.Millisecond):
	}
}

func mustAcquireNow(t *testing.T, g *agentGate, class AgentClass) func() {
	t.Helper()
	rel, err := g.acquire(context.Background(), class, func(WaitReason) {
		t.Fatal("onWait called with free capacity")
	})
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	return rel
}

func gateCounts(g *agentGate) (running, waiting int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.running, len(g.waiting[ClassInteractive]) + len(g.waiting[ClassBackground])
}

func TestGateLimit(t *testing.T) {
	g := newGate(2, 0, nil, quietLog())
	r1 := mustAcquireNow(t, g, ClassInteractive)
	r2 := mustAcquireNow(t, g, ClassInteractive)

	third := acquireAsync(g, context.Background(), ClassInteractive)
	if r := third.waiting(t); r != WaitSlot {
		t.Fatalf("wait reason = %v, want slot", r)
	}
	third.stillWaiting(t)

	r1()
	r3 := third.admitted(t)
	if run, wait := gateCounts(g); run != 2 || wait != 0 {
		t.Fatalf("running=%d waiting=%d, want 2, 0", run, wait)
	}
	r2()
	r3()
	if run, _ := gateCounts(g); run != 0 {
		t.Fatalf("running=%d after all released", run)
	}
}

func TestGateMaxBelowOneIsOne(t *testing.T) {
	g := newGate(0, 0, nil, quietLog())
	rel := mustAcquireNow(t, g, ClassInteractive)
	p := acquireAsync(g, context.Background(), ClassInteractive)
	p.waiting(t)
	rel()
	p.admitted(t)()
}

func TestGateInteractiveBeforeBackground(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	hold := mustAcquireNow(t, g, ClassInteractive)

	bg := acquireAsync(g, context.Background(), ClassBackground)
	bg.waiting(t)
	in := acquireAsync(g, context.Background(), ClassInteractive)
	in.waiting(t)

	hold()
	relIn := in.admitted(t)
	bg.stillWaiting(t)
	relIn()
	bg.admitted(t)()
}

func TestGateFIFOWithinClass(t *testing.T) {
	for _, class := range []AgentClass{ClassInteractive, ClassBackground} {
		t.Run(class.String(), func(t *testing.T) {
			g := newGate(1, 0, nil, quietLog())
			hold := mustAcquireNow(t, g, class)
			var ps []*pendingAcquire
			for i := 0; i < 4; i++ {
				p := acquireAsync(g, context.Background(), class)
				p.waiting(t) // registered before the next one starts
				ps = append(ps, p)
			}
			rel := hold
			for i, p := range ps {
				rel()
				rel = p.admitted(t)
				for _, later := range ps[i+1:] {
					select {
					case <-later.done:
						t.Fatalf("waiter %d admitted out of order", i+1)
					default:
					}
				}
			}
			rel()
		})
	}
}

func TestGateBackgroundDoesNotPreempt(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	bgRel := mustAcquireNow(t, g, ClassBackground)
	in := acquireAsync(g, context.Background(), ClassInteractive)
	in.waiting(t)
	in.stillWaiting(t) // the running background agent keeps its slot
	bgRel()
	in.admitted(t)()
}

func TestGateCancelWhileWaiting(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	hold := mustAcquireNow(t, g, ClassInteractive)

	ctx, cancel := context.WithCancel(context.Background())
	first := acquireAsync(g, ctx, ClassInteractive)
	first.waiting(t)
	second := acquireAsync(g, context.Background(), ClassInteractive)
	second.waiting(t)

	cancel()
	select {
	case <-first.done:
	case <-time.After(time.Second):
		t.Fatal("cancelled waiter did not leave promptly")
	}
	if !errors.Is(first.err, context.Canceled) || first.release != nil {
		t.Fatalf("cancelled acquire = (%v, release set %v)", first.err, first.release != nil)
	}
	if run, wait := gateCounts(g); run != 1 || wait != 1 {
		t.Fatalf("after cancel running=%d waiting=%d, want 1, 1", run, wait)
	}

	// The place goes to the next waiter, not to the cancelled one.
	hold()
	relSecond := second.admitted(t)
	if run, wait := gateCounts(g); run != 1 || wait != 0 {
		t.Fatalf("running=%d waiting=%d, want 1, 0", run, wait)
	}
	relSecond()
	mustAcquireNow(t, g, ClassInteractive)()
}

// A cancelled head waiter must wake the waiter behind it, which may be
// admitted at once (no place handed to nobody).
func TestGateCancelledHeadUnblocksNext(t *testing.T) {
	var mem memValue
	mem.mb.Store(100) // low
	mem.ok.Store(1)
	g := newGate(2, 900, mem.read, quietLog())
	g.poll = time.Hour // only a wake-up can move a waiter
	g.memMaxWait = time.Hour

	ctx, cancel := context.WithCancel(context.Background())
	head := acquireAsync(g, ctx, ClassBackground)
	if r := head.waiting(t); r != WaitMemory {
		t.Fatalf("head reason = %v, want memory", r)
	}
	next := acquireAsync(g, context.Background(), ClassBackground)
	if r := next.waiting(t); r != WaitSlot {
		t.Fatalf("reason behind head = %v, want slot (queued)", r)
	}
	mem.mb.Store(2000) // recovered, but nobody polls for an hour
	cancel()
	<-head.done
	next.admitted(t)()
}

func TestGateCancelledBeforeAcquire(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := g.acquire(ctx, ClassInteractive, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if run, wait := gateCounts(g); run != 0 || wait != 0 {
		t.Fatalf("running=%d waiting=%d", run, wait)
	}
}

func TestGateReleaseIdempotent(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	rel := mustAcquireNow(t, g, ClassInteractive)
	rel()
	rel()
	if run, _ := gateCounts(g); run != 0 {
		t.Fatalf("running=%d after double release", run)
	}
	held := mustAcquireNow(t, g, ClassInteractive)
	p := acquireAsync(g, context.Background(), ClassInteractive)
	p.waiting(t) // a double release must not have made room for two
	held()
	p.admitted(t)()
}

type memValue struct{ mb, ok atomic.Int64 }

func (m *memValue) read() (int64, bool) { return m.mb.Load(), m.ok.Load() == 1 }

func TestGateWaitsForMemoryThenProceeds(t *testing.T) {
	var mem memValue
	mem.mb.Store(500)
	mem.ok.Store(1)
	g := newGate(2, 900, mem.read, quietLog())
	g.poll = 5 * time.Millisecond
	g.memMaxWait = time.Hour

	p := acquireAsync(g, context.Background(), ClassInteractive)
	if r := p.waiting(t); r != WaitMemory {
		t.Fatalf("reason = %v, want memory", r)
	}
	p.stillWaiting(t)
	mem.mb.Store(1200)
	p.admitted(t)()
	if n := len(p.waits); n != 0 {
		t.Fatalf("onWait called %d more times while polling memory", n)
	}
}

func TestGateUnreadableMemoryIsSufficient(t *testing.T) {
	var mem memValue // ok = 0
	g := newGate(1, 900, mem.read, quietLog())
	mustAcquireNow(t, g, ClassInteractive)()
}

func TestGateZeroThresholdDisablesMemoryCheck(t *testing.T) {
	var mem memValue
	mem.mb.Store(1)
	mem.ok.Store(1)
	g := newGate(1, 0, mem.read, quietLog())
	mustAcquireNow(t, g, ClassInteractive)()
}

func TestGateBoundedMemoryWaitOnIdleSystem(t *testing.T) {
	var mem memValue
	mem.mb.Store(100)
	mem.ok.Store(1)
	var logs syncBuffer
	g := newGate(2, 900, mem.read, slog.New(slog.NewTextHandler(&logs, nil)))
	g.poll = 5 * time.Millisecond
	g.memMaxWait = 60 * time.Millisecond

	start := time.Now()
	p := acquireAsync(g, context.Background(), ClassInteractive)
	p.waiting(t)
	p.admitted(t)()
	if d := time.Since(start); d < g.memMaxWait {
		t.Fatalf("admitted after %v, before the bound %v", d, g.memMaxWait)
	}
	if !strings.Contains(logs.String(), "level=WARN") {
		t.Fatalf("no Warn logged for the override:\n%s", logs.String())
	}
}

// The bound applies only when nothing runs: a busy system keeps the
// waiter waiting for memory, since a finishing agent frees it.
func TestGateBoundedMemoryWaitNotWhileAgentsRun(t *testing.T) {
	var mem memValue
	mem.mb.Store(2000)
	mem.ok.Store(1)
	g := newGate(2, 900, mem.read, quietLog())
	g.poll = 5 * time.Millisecond
	g.memMaxWait = 20 * time.Millisecond

	running := mustAcquireNow(t, g, ClassInteractive)
	mem.mb.Store(100)
	p := acquireAsync(g, context.Background(), ClassInteractive)
	if r := p.waiting(t); r != WaitMemory {
		t.Fatalf("reason = %v", r)
	}
	time.Sleep(3 * g.memMaxWait)
	p.stillWaiting(t)
	running() // now idle: the bound has passed, so it proceeds
	p.admitted(t)()
}

func TestGateOnWaitOnce(t *testing.T) {
	g := newGate(1, 0, nil, quietLog())
	hold := mustAcquireNow(t, g, ClassInteractive)
	var calls atomic.Int32
	done := make(chan struct{})
	var rel func()
	go func() {
		defer close(done)
		rel, _ = g.acquire(context.Background(), ClassBackground, func(WaitReason) { calls.Add(1) })
	}()
	// Several wake-ups while it stays queued behind interactive traffic.
	for i := 0; i < 5; i++ {
		waitFor(t, func() bool { return calls.Load() >= 1 })
		in := mustAcquireAfter(t, g, hold)
		hold = in
	}
	hold()
	<-done
	rel()
	if n := calls.Load(); n != 1 {
		t.Fatalf("onWait called %d times, want 1", n)
	}
}

// mustAcquireAfter queues an interactive acquire, frees prev, and
// returns the new holder's release.
func mustAcquireAfter(t *testing.T, g *agentGate, prev func()) func() {
	t.Helper()
	p := acquireAsync(g, context.Background(), ClassInteractive)
	p.waiting(t)
	prev()
	return p.admitted(t)
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met")
		}
		time.Sleep(time.Millisecond)
	}
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// One change can make room for several waiters: when memory recovers,
// the head is admitted on its poll and must wake the waiter behind it,
// which does not poll.
func TestGateAdmissionWakesNextWaiter(t *testing.T) {
	var mem memValue
	mem.mb.Store(100)
	mem.ok.Store(1)
	g := newGate(2, 900, mem.read, quietLog())
	g.poll = 5 * time.Millisecond
	g.memMaxWait = time.Hour

	first := acquireAsync(g, context.Background(), ClassInteractive)
	first.waiting(t)
	second := acquireAsync(g, context.Background(), ClassInteractive)
	second.waiting(t)
	mem.mb.Store(2000)
	relFirst := first.admitted(t)
	defer relFirst() // still running: only the admission can wake second
	second.admitted(t)()
}
