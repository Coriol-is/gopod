package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestDrainPreservesActiveAndPendingIngress(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	next := make(chan struct{})
	var n atomic.Int32
	q := New(t.Context(), func(ctx context.Context, items []Item) error {
		if n.Add(1) == 1 {
			close(entered)
			select {
			case <-finish:
				return nil
			case <-ctx.Done():
				t.Error("drain cancelled active work")
				return ctx.Err()
			}
		}
		close(next)
		return nil
	}, 1, nil, nil)
	defer q.Close()
	q.Enqueue(t.Context(), Item{Folder: "owner", Text: "first"})
	<-entered
	q.BeginDrain()
	q.Enqueue(t.Context(), Item{Folder: "owner", Text: "second"})
	c, cancel := context.WithTimeout(t.Context(), 10*time.Millisecond)
	defer cancel()
	if !errors.Is(q.WaitDrained(c), context.DeadlineExceeded) {
		t.Fatal("busy drain completed")
	}
	close(finish)
	if err := q.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
	if q.Pending() != 1 || n.Load() != 1 {
		t.Fatal("pending input lost or ran", q.Pending(), n.Load())
	}
	q.CancelDrain()
	select {
	case <-next:
	case <-time.After(time.Second):
		t.Fatal("cancel did not resume queue")
	}
}
func TestForceIntentBeforeCancellationAndPendingForegroundRetained(t *testing.T) {
	entered := make(chan struct{})
	cancelled := make(chan struct{})
	var persisted atomic.Bool
	q := New(t.Context(), func(ctx context.Context, items []Item) error {
		close(entered)
		<-ctx.Done()
		if !persisted.Load() {
			t.Error("cancel preceded durable intent")
		}
		close(cancelled)
		return ctx.Err()
	}, 1, nil, nil)
	defer q.Close()
	q.Enqueue(t.Context(), Item{ID: 1, Folder: "owner", Source: "telegram"})
	<-entered
	q.Enqueue(t.Context(), Item{ID: 2, Folder: "owner", Source: "task"})
	q.Enqueue(t.Context(), Item{ID: 3, Folder: "owner", Source: "telegram"})
	if err := q.ForceDrain(t.Context(), func(items []Item) error {
		if len(items) != 2 {
			t.Fatalf("cutoff=%v", items)
		}
		persisted.Store(true)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	<-cancelled
	if err := q.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
	if q.Pending() != 1 {
		t.Fatal("foreground queue was lost", q.Pending())
	}
}
func TestForcePersistFailureDoesNotSignal(t *testing.T) {
	entered := make(chan struct{})
	finish := make(chan struct{})
	q := New(t.Context(), func(ctx context.Context, _ []Item) error {
		close(entered)
		select {
		case <-ctx.Done():
			t.Error("cancel on failed durable intent")
			return ctx.Err()
		case <-finish:
			return nil
		}
	}, 1, nil, nil)
	defer q.Close()
	q.Enqueue(t.Context(), Item{Folder: "owner"})
	<-entered
	if err := q.ForceDrain(t.Context(), func([]Item) error { return errors.New("disk full") }); err == nil {
		t.Fatal("missing error")
	}
	close(finish)
	if err := q.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
}

// A batch that fails while a drain is pending is finalized at once instead of
// entering the retry/backoff loop, so WaitDrained returns and the restart can
// proceed. Nothing is replayed automatically after the restart.
func TestDrainTimeFailureFinalizesWithoutRetry(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var n atomic.Int32
	q := New(t.Context(), func(ctx context.Context, items []Item) error {
		if n.Add(1) == 1 {
			close(entered)
			<-release
		}
		return errors.New("agent failed")
	}, 1, nil, nil)
	defer q.Close()
	q.Enqueue(t.Context(), Item{Folder: "owner", Text: "first"})
	<-entered
	q.BeginDrain()
	close(release)
	c, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := q.WaitDrained(c); err != nil {
		t.Fatalf("drain did not complete after failure: %v", err)
	}
	if n.Load() != 1 {
		t.Fatalf("failed batch retried during drain: %d runs", n.Load())
	}
}
