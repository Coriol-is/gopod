package maintenance

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type fakeLister struct {
	ids []string
	err error
}

func (f fakeLister) LiveOwned(context.Context) ([]string, error) { return f.ids, f.err }

func lockPath(t *testing.T) string { return filepath.Join(t.TempDir(), ControllerLockName) }

func TestTryLockExclusiveAndReleasable(t *testing.T) {
	p := lockPath(t)
	a, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TryLock(p); !errors.Is(err, ErrHeld) {
		t.Fatalf("second lock: %v", err)
	}
	if err := a.Release(); err != nil {
		t.Fatal(err)
	}
	if err := a.Release(); err != nil {
		t.Fatalf("double release: %v", err)
	}
	b, err := TryLock(p)
	if err != nil {
		t.Fatalf("after release: %v", err)
	}
	_ = b.Release()
}

func TestLockWaitBlocksUntilReleased(t *testing.T) {
	p := lockPath(t)
	a, _ := TryLock(p)
	go func() { time.Sleep(300 * time.Millisecond); _ = a.Release() }()
	l, err := LockWait(context.Background(), p, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Release()
	c, _ := TryLock(p)
	if _, err := LockWait(context.Background(), p, 100*time.Millisecond); !errors.Is(err, ErrHeld) {
		t.Fatalf("expected ErrHeld, got %v", err)
	}
	_ = c.Release()
}

func TestAcquireRefusesWhileControllerHoldsLockAndDoesNotTouchIt(t *testing.T) {
	p := lockPath(t)
	controller, _ := TryLock(p)
	defer controller.Release()
	_, err := Settlement{LockPath: p, AssertNoContainers: true}.Acquire(context.Background())
	var ue *UnsettledError
	if !errors.Is(err, ErrUnsettled) || !errors.As(err, &ue) {
		t.Fatalf("err = %v", err)
	}
	// The controller still owns the lock: the refusal did not disturb it.
	if _, err := TryLock(p); !errors.Is(err, ErrHeld) {
		t.Fatalf("controller lock was disturbed: %v", err)
	}
}

func TestAcquireRefusesLiveContainersUnknownInventoryAndUnassertedSet(t *testing.T) {
	ctx := context.Background()
	cases := map[string]Settlement{
		"live containers":    {Containers: fakeLister{ids: []string{"0123456789abcdef"}}},
		"inventory error":    {Containers: fakeLister{err: errors.New("docker down")}},
		"unknown writer set": {},
	}
	for name, s := range cases {
		s.LockPath = lockPath(t)
		if _, err := s.Acquire(ctx); !errors.Is(err, ErrUnsettled) {
			t.Fatalf("%s: err = %v", name, err)
		}
		// A refusal must leave the lock free again.
		l, err := TryLock(s.LockPath)
		if err != nil {
			t.Fatalf("%s: lock leaked: %v", name, err)
		}
		_ = l.Release()
	}
}

func TestAcquireSettledHoldsFenceBlockingControllerStart(t *testing.T) {
	p := lockPath(t)
	f, err := Settlement{LockPath: p, Containers: fakeLister{}}.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if f.ContainerEvidence != ContainersNoneRunning {
		t.Fatalf("evidence %q", f.ContainerEvidence)
	}
	if _, err := LockWait(context.Background(), p, 50*time.Millisecond); !errors.Is(err, ErrHeld) {
		t.Fatalf("controller could start during capture: %v", err)
	}
	_ = f.Release()
	l, err := TryLock(p)
	if err != nil {
		t.Fatal(err)
	}
	_ = l.Release()
	a, err := Settlement{LockPath: p, AssertNoContainers: true}.Acquire(context.Background())
	if err != nil || a.ContainerEvidence != ContainersOperatorAsserted {
		t.Fatalf("asserted: %v %v", a, err)
	}
	_ = a.Release()
}

func TestAcquireRaceNeverTwoHolders(t *testing.T) {
	p := lockPath(t)
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, holders := 0, 0
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, err := Settlement{LockPath: p, AssertNoContainers: true}.Acquire(context.Background())
			if err == nil {
				mu.Lock()
				wins++
				holders++
				if holders > 1 {
					t.Error("two fences held at once")
				}
				mu.Unlock()
				time.Sleep(100 * time.Millisecond)
				mu.Lock()
				holders--
				mu.Unlock()
				_ = f.Release()
			}
		}()
	}
	wg.Wait()
	if wins < 1 {
		t.Fatal("nobody acquired")
	}
}
