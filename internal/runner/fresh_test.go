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

type stuckFreshExec struct {
	started     chan context.Context
	killStarted chan struct{}
	killRelease chan struct{}
	once        sync.Once
}

func (f *stuckFreshExec) Exec(ctx context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	if isKillCmd(cmd) {
		f.once.Do(func() { close(f.killStarted) })
		<-f.killRelease
		return &ExecResult{}, nil
	}
	if len(cmd) > 4 && strings.Contains(cmd[2], `echo $$ >`) {
		f.started <- ctx
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &ExecResult{}, nil
}
func (*stuckFreshExec) ExecStream(context.Context, string, []string, []string) (*StreamHandle, error) {
	panic("unexpected streaming call")
}
func freshFixture(t *testing.T) (*Runner, *stuckFreshExec) {
	t.Helper()
	f := &stuckFreshExec{started: make(chan context.Context, 1), killStarted: make(chan struct{}), killRelease: make(chan struct{})}
	r := &Runner{ex: f, provider: NewCodexProvider("img"), log: quietLog(), activity: make(map[string]time.Time), turnCount: make(map[convKey]int)}
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) { return "cid", nil }
	return r, f
}

func TestForegroundCancelsSameChatFreshAndWaitsForProcessCleanup(t *testing.T) {
	r, f := freshFixture(t)
	done := make(chan error, 1)
	go func() {
		_, err := r.RunFresh(context.Background(), "chat", TierRegistered, nil, "extract")
		done <- err
	}()
	ctx := recvOrFail(t, f.started)
	if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > freshRunTimeout {
		t.Fatal("system run has no bounded deadline")
	}
	other, err := r.acquireAgentSlot(context.Background(), "other")
	if err != nil {
		t.Fatal(err)
	}
	other()
	if ctx.Err() != nil {
		t.Fatal("another chat canceled background")
	}
	acquired := make(chan func(), 1)
	go func() {
		release, err := r.acquireAgentSlot(context.Background(), "chat")
		if err == nil {
			acquired <- release
		}
	}()
	recvOrFail(t, f.killStarted)
	select {
	case <-acquired:
		t.Fatal("foreground started before old process cleanup")
	default:
	}
	select {
	case <-done:
		t.Fatal("background slot released before kill finished")
	default:
	}
	close(f.killRelease)
	if err := recvOrFail(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("background error=%v", err)
	}
	release := recvOrFail(t, acquired)
	release()
}

func TestFreshCallerDeadlineKillsAndReleasesSlot(t *testing.T) {
	r, f := freshFixture(t)
	close(f.killRelease)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err := r.RunFresh(ctx, "chat", TierRegistered, nil, "extract")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error=%v", err)
	}
	recvOrFail(t, f.killStarted)
	release, err := r.acquireAgentSlot(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestCancelledCompactionPreservesConversation(t *testing.T) {
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			st := newFakeSessionStore()
			st.PutSession(context.Background(), "chat", 7, "codex", "existing-session")
			r := &Runner{store: st, provider: NewCodexProvider("img"), log: quietLog(), turnCount: map[convKey]int{{"chat", 7}: 2}}
			r.SetCompactFn(func(context.Context, string, int) error { return cause })
			_, err := r.CompactSession(context.Background(), "chat", 7, TierRegistered, nil)
			if !errors.Is(err, cause) || st.row("chat", 7, "codex") != "existing-session" || r.getTurnCount("chat", 7) != 2 {
				t.Fatalf("canceled compaction cleared state: %v", err)
			}
		})
	}
}

func TestSyncRunCancellationWaitsForProcessCleanup(t *testing.T) {
	r, f := freshFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := r.Run(ctx, "chat", 7, TierRegistered, nil, "user request"); done <- err }()
	recvOrFail(t, f.started)
	cancel()
	recvOrFail(t, f.killStarted)
	next := make(chan func(), 1)
	go func() {
		release, err := r.acquireAgentSlot(context.Background(), "chat")
		if err == nil {
			next <- release
		}
	}()
	select {
	case <-next:
		t.Fatal("sync slot released with old process still cleaning up")
	default:
	}
	close(f.killRelease)
	if err := recvOrFail(t, done); !errors.Is(err, context.Canceled) {
		t.Fatalf("sync error=%v", err)
	}
	release := recvOrFail(t, next)
	release()
}
