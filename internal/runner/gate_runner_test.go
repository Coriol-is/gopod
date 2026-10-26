package runner

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
)

// gatedRunner is a Runner on the fake exec with an agent gate of max
// slots and no memory check. Streamed agents are fakeProcs on
// fx.started; RunFresh's agent exec blocks until freshGo receives.
type gatedRunner struct {
	r          *Runner
	fx         *fakeAgentExec
	freshStart chan string   // chat of each RunFresh agent exec, when it starts
	freshGo    chan struct{} // one receive lets one RunFresh exec finish
	notices    map[string][]WaitReason
	mu         sync.Mutex
}

func newGatedRunner(t *testing.T, max int) *gatedRunner {
	t.Helper()
	g := &gatedRunner{
		fx:         newFakeAgentExec(),
		freshStart: make(chan string, 8),
		freshGo:    make(chan struct{}),
		notices:    make(map[string][]WaitReason),
	}
	g.fx.started = make(chan *fakeProc, 8)
	g.fx.onExec = func(cmd []string) *ExecResult {
		if len(cmd) > 4 && cmd[0] == "sh" && strings.Contains(cmd[2], `echo $$ >`) {
			cmd = cmd[4:]
		}
		if len(cmd) > 0 && cmd[0] == "claude" && strings.HasPrefix(cmd[len(cmd)-1], "bg") { // RunFresh's agent
			g.freshStart <- cmd[len(cmd)-1]
			<-g.freshGo
			return &ExecResult{Stdout: "fresh"}
		}
		return nil
	}
	g.r = &Runner{profilePolicy: testPersonalPolicy(),
		ex:             g.fx,
		provider:       NewClaudeProvider("img"),
		log:            quietLog(),
		turnCount:      make(map[convKey]int),
		activity:       make(map[string]time.Time),
		containerLocks: make(map[string]*sync.Mutex),
		gate:           newGate(max, 0, nil, quietLog()),
	}
	g.r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		return "cid", nil
	}
	return g
}

// ctxFor returns ctx with a WaitNotice recording waits under chat.
func (g *gatedRunner) ctxFor(ctx context.Context, chat string) context.Context {
	return WithWaitNotice(ctx, func(r WaitReason) {
		g.mu.Lock()
		g.notices[chat] = append(g.notices[chat], r)
		g.mu.Unlock()
	})
}

func (g *gatedRunner) noticesFor(chat string) []WaitReason {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]WaitReason(nil), g.notices[chat]...)
}

type streamStart struct {
	stream *AgentStream
	err    error
	done   chan struct{}
}

// runStreamAsync calls RunStream for chat in a goroutine (it blocks
// while the turn waits for the gate).
func (g *gatedRunner) runStreamAsync(ctx context.Context, chat string) *streamStart {
	s := &streamStart{done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.stream, s.err = g.r.RunStream(g.ctxFor(ctx, chat), chat, 0, TierRegistered, nil, "hi "+chat)
	}()
	return s
}

func (g *gatedRunner) nextProc(t *testing.T) *fakeProc {
	t.Helper()
	select {
	case p := <-g.fx.started:
		return p
	case <-time.After(2 * time.Second):
		t.Fatal("no agent started")
	}
	return nil
}

func (g *gatedRunner) noProc(t *testing.T) {
	t.Helper()
	select {
	case p := <-g.fx.started:
		t.Fatalf("agent started while it should wait: %q", p.cmd)
	case <-time.After(50 * time.Millisecond):
	}
}

func waitNotice(t *testing.T, g *gatedRunner, chat string) WaitReason {
	t.Helper()
	waitFor(t, func() bool { return len(g.noticesFor(chat)) > 0 })
	return g.noticesFor(chat)[0]
}

func streamOK(t *testing.T, s *streamStart) *AgentStream {
	t.Helper()
	select {
	case <-s.done:
	case <-time.After(2 * time.Second):
		t.Fatal("RunStream did not return")
	}
	if s.err != nil {
		t.Fatalf("RunStream: %v", s.err)
	}
	return s.stream
}

func finish(t *testing.T, p *fakeProc, stream *AgentStream) {
	t.Helper()
	p.exit(0)
	if res := readResult(t, stream); res.Err != nil {
		t.Fatalf("result: %v", res.Err)
	}
}

func TestRunStreamThirdTurnWaitsForGate(t *testing.T) {
	g := newGatedRunner(t, 2)
	a := g.runStreamAsync(context.Background(), "a")
	pa := g.nextProc(t)
	sa := streamOK(t, a)
	b := g.runStreamAsync(context.Background(), "b")
	pb := g.nextProc(t)
	sb := streamOK(t, b)

	c := g.runStreamAsync(context.Background(), "c")
	if r := waitNotice(t, g, "c"); r != WaitSlot {
		t.Fatalf("c waits for %v, want slot", r)
	}
	g.noProc(t)

	finish(t, pa, sa)
	pc := g.nextProc(t)
	sc := streamOK(t, c)
	finish(t, pb, sb)
	finish(t, pc, sc)

	if n := len(g.noticesFor("c")); n != 1 {
		t.Fatalf("c notices = %d, want 1", n)
	}
	for _, chat := range []string{"a", "b"} {
		if n := len(g.noticesFor(chat)); n != 0 {
			t.Errorf("%s was told to wait with a free slot", chat)
		}
	}
	waitFor(t, func() bool { run, _ := g.r.gate.counts(); return run == 0 })
}

func TestSingleTurnWithCapacityNeverWaits(t *testing.T) {
	g := newGatedRunner(t, DefaultMaxAgents)
	s := g.runStreamAsync(context.Background(), "a")
	p := g.nextProc(t)
	stream := streamOK(t, s)
	go func() {
		p.stdoutW.Write([]byte("hello"))
		p.exit(0)
	}()
	res := readResult(t, stream)
	if res.Err != nil || res.FullText != "hello" {
		t.Fatalf("result = %+v", res)
	}
	if n := len(g.noticesFor("a")); n != 0 {
		t.Fatalf("onWait called %d times with ample capacity", n)
	}
	waitFor(t, func() bool { run, _ := g.r.gate.counts(); return run == 0 })
}

func TestRunFreshDoesNotStarveWaitingTurn(t *testing.T) {
	g := newGatedRunner(t, 1)

	// A background run holds the only slot; a second one queues.
	fresh := make(chan error, 2)
	go func() {
		_, err := g.r.RunFresh(context.Background(), "bg1", TierRegistered, nil, "bg1")
		fresh <- err
	}()
	if got := recvOrFail(t, g.freshStart); got != "bg1" {
		t.Fatalf("first fresh = %q", got)
	}
	go func() {
		_, err := g.r.RunFresh(g.ctxFor(context.Background(), "bg2"), "bg2", TierRegistered, nil, "bg2")
		fresh <- err
	}()
	waitNotice(t, g, "bg2")

	// A user turn arrives after it and must go first.
	turn := g.runStreamAsync(context.Background(), "user")
	waitNotice(t, g, "user")
	g.noProc(t) // the running background agent is not preempted

	g.freshGo <- struct{}{} // bg1 finishes
	if err := recvOrFail(t, fresh); err != nil {
		t.Fatalf("bg1: %v", err)
	}
	p := g.nextProc(t)
	stream := streamOK(t, turn)
	select {
	case chat := <-g.freshStart:
		t.Fatalf("background %q started before the waiting user turn finished", chat)
	case <-time.After(50 * time.Millisecond):
	}
	finish(t, p, stream)

	if got := recvOrFail(t, g.freshStart); got != "bg2" {
		t.Fatalf("second fresh = %q", got)
	}
	g.freshGo <- struct{}{}
	if err := recvOrFail(t, fresh); err != nil {
		t.Fatalf("bg2: %v", err)
	}
}

func TestRunWaitsForGate(t *testing.T) {
	g := newGatedRunner(t, 1)
	s := g.runStreamAsync(context.Background(), "a")
	p := g.nextProc(t)
	stream := streamOK(t, s)

	done := make(chan error, 1)
	go func() {
		_, err := g.r.Run(g.ctxFor(context.Background(), "b"), "b", 0, TierRegistered, nil, "hi")
		done <- err
	}()
	if r := waitNotice(t, g, "b"); r != WaitSlot {
		t.Fatalf("reason = %v", r)
	}
	select {
	case err := <-done:
		t.Fatalf("Run returned while the gate was full: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	finish(t, p, stream)
	if err := recvOrFail(t, done); err != nil {
		t.Fatalf("Run: %v", err)
	}
	waitFor(t, func() bool { run, _ := g.r.gate.counts(); return run == 0 })
}

// Stop while a turn waits for the gate: RunStream returns at once with
// the cancellation, no agent starts, the chat's agent slot is free and
// the gate holds no ghost place.
func TestStopWhileWaitingForGate(t *testing.T) {
	g := newGatedRunner(t, 1)
	s := g.runStreamAsync(context.Background(), "a")
	pa := g.nextProc(t)
	sa := streamOK(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	b := g.runStreamAsync(ctx, "b")
	waitNotice(t, g, "b")
	cancel()
	select {
	case <-b.done:
	case <-time.After(time.Second):
		t.Fatal("RunStream did not return promptly after Stop")
	}
	if !errors.Is(b.err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", b.err)
	}
	g.noProc(t)
	if run, wait := g.r.gate.counts(); run != 1 || wait != 0 {
		t.Fatalf("gate running=%d waiting=%d, want 1, 0", run, wait)
	}

	finish(t, pa, sa)
	waitFor(t, func() bool { run, _ := g.r.gate.counts(); return run == 0 })

	// Chat b's agent slot was released: its next turn starts at once.
	next := g.runStreamAsync(context.Background(), "b")
	pb := g.nextProc(t)
	finish(t, pb, streamOK(t, next))
	if n := len(g.noticesFor("b")); n != 1 {
		t.Fatalf("b notices = %d, want 1 (only the stopped turn waited)", n)
	}
}

// Stop while a turn waits for its chat's previous agent (the stage
// before the gate) leaves the gate untouched.
func TestStopWhileWaitingForChatSlot(t *testing.T) {
	g := newGatedRunner(t, 2)
	s := g.runStreamAsync(context.Background(), "a")
	pa := g.nextProc(t)
	sa := streamOK(t, s)

	ctx, cancel := context.WithCancel(context.Background())
	second := g.runStreamAsync(ctx, "a")
	g.noProc(t) // waits for chat a's agent, not for the gate
	cancel()
	select {
	case <-second.done:
	case <-time.After(time.Second):
		t.Fatal("RunStream did not return promptly")
	}
	if !errors.Is(second.err, context.Canceled) {
		t.Fatalf("err = %v", second.err)
	}
	if run, wait := g.r.gate.counts(); run != 1 || wait != 0 {
		t.Fatalf("gate running=%d waiting=%d, want 1, 0", run, wait)
	}
	if n := len(g.noticesFor("a")); n != 0 {
		t.Fatalf("gate notice for a turn that never reached the gate")
	}
	finish(t, pa, sa)
	waitFor(t, func() bool { run, _ := g.r.gate.counts(); return run == 0 })
}

// A stopped streamed agent keeps its gate slot until the kill is over:
// the memory is not free before the process is gone.
func TestGateHeldUntilCancelledAgentIsKilled(t *testing.T) {
	g := newGatedRunner(t, 1)
	g.fx.killGate = make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	s := g.runStreamAsync(ctx, "a")
	g.nextProc(t)
	stream := streamOK(t, s)
	go func() {
		for range stream.Chunks {
		}
	}()
	cancel()

	other := g.runStreamAsync(context.Background(), "b")
	waitNotice(t, g, "b")
	g.noProc(t)
	close(g.fx.killGate) // the kill finishes, the process is gone
	pb := g.nextProc(t)
	finish(t, pb, streamOK(t, other))
}

func recvOrFail[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting on a channel")
	}
	var zero T
	return zero
}
