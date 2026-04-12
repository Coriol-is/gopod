package queue

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func silentLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestSerializationPerChat(t *testing.T) {
	// Verify that handler calls for the same folder are serialized
	// (max concurrent = 1). Coalescing means 3 enqueued items may
	// produce fewer than 3 handler calls.
	var concurrent int32
	var maxConcurrent int32
	var totalItems int32
	done := make(chan struct{})

	handler := func(ctx context.Context, items []Item) error {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
				break
			}
		}
		time.Sleep(20 * time.Millisecond) // simulate work
		atomic.AddInt32(&concurrent, -1)
		if atomic.AddInt32(&totalItems, int32(len(items))) >= 3 {
			close(done)
		}
		return nil
	}

	q := New(handler, 10, silentLog())
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		q.Enqueue(ctx, Item{Folder: "alice", Text: "msg"})
	}

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for all items to be processed")
	}
	if mc := atomic.LoadInt32(&maxConcurrent); mc > 1 {
		t.Errorf("max concurrent for same chat = %d, want 1", mc)
	}
}

func TestGlobalConcurrencyCap(t *testing.T) {
	// With cap=2, three different chats should not exceed 2 concurrent.
	var concurrent int32
	var maxConcurrent int32
	var totalCalls int32
	done := make(chan struct{})

	handler := func(ctx context.Context, items []Item) error {
		n := atomic.AddInt32(&concurrent, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if n <= old {
				break
			}
			if atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
				break
			}
		}
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&concurrent, -1)
		if atomic.AddInt32(&totalCalls, 1) >= 3 {
			close(done)
		}
		return nil
	}

	q := New(handler, 2, silentLog())
	ctx := context.Background()

	q.Enqueue(ctx, Item{Folder: "a", Text: "msg"})
	q.Enqueue(ctx, Item{Folder: "b", Text: "msg"})
	q.Enqueue(ctx, Item{Folder: "c", Text: "msg"})

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	if mc := atomic.LoadInt32(&maxConcurrent); mc > 2 {
		t.Errorf("max global concurrent = %d, want ≤ 2", mc)
	}
}

func TestCoalescing(t *testing.T) {
	// Enqueue 3 items while handler is busy with the first. The
	// second handler call should receive all 3 pending items coalesced.
	var calls int32
	var secondCallItems int

	firstDone := make(chan struct{})
	allDone := make(chan struct{})

	handler := func(ctx context.Context, items []Item) error {
		n := atomic.AddInt32(&calls, 1)
		if n == 1 {
			// First call: one item. Signal so test can enqueue more.
			close(firstDone)
			time.Sleep(50 * time.Millisecond) // hold the slot
		} else {
			// Second call: should have the coalesced items.
			secondCallItems = len(items)
			close(allDone)
		}
		return nil
	}

	q := New(handler, 10, silentLog())
	ctx := context.Background()

	q.Enqueue(ctx, Item{Folder: "alice", Text: "first"})
	<-firstDone

	// Enqueue 3 more while the first is still running.
	q.Enqueue(ctx, Item{Folder: "alice", Text: "second"})
	q.Enqueue(ctx, Item{Folder: "alice", Text: "third"})
	q.Enqueue(ctx, Item{Folder: "alice", Text: "fourth"})

	select {
	case <-allDone:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for second handler call")
	}

	totalCalls := atomic.LoadInt32(&calls)
	if totalCalls != 2 {
		t.Errorf("handler called %d times, want 2 (one for first msg, one coalesced for rest)", totalCalls)
	}
	if secondCallItems != 3 {
		t.Errorf("second call got %d items, want 3 (coalesced)", secondCallItems)
	}
}

func TestBackoffAndRetry(t *testing.T) {
	var calls int32
	done := make(chan struct{})

	handler := func(ctx context.Context, items []Item) error {
		n := atomic.AddInt32(&calls, 1)
		if n <= 2 {
			return fmt.Errorf("transient failure %d", n)
		}
		close(done)
		return nil // succeed on 3rd try
	}

	q := New(handler, 10, silentLog())
	q.maxRetries = 5
	// Fast backoff for testing — 1ms instead of 5s+.
	q.BackoffFunc = func(retry int) time.Duration { return time.Millisecond }
	ctx := context.Background()

	q.Enqueue(ctx, Item{Folder: "alice", Text: "msg"})

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for successful retry")
	}

	total := atomic.LoadInt32(&calls)
	if total < 3 {
		t.Errorf("calls = %d, want >= 3 (2 failures + 1 success)", total)
	}
}

func TestPendingAndActiveChats(t *testing.T) {
	block := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		<-block
		return nil
	}

	q := New(handler, 10, silentLog())
	ctx := context.Background()

	q.Enqueue(ctx, Item{Folder: "alice", Text: "msg"})
	time.Sleep(20 * time.Millisecond) // let worker start

	if q.ActiveChats() != 1 {
		t.Errorf("ActiveChats = %d, want 1", q.ActiveChats())
	}

	q.Enqueue(ctx, Item{Folder: "alice", Text: "msg2"})
	if q.Pending() != 1 {
		t.Errorf("Pending = %d, want 1", q.Pending())
	}

	close(block)
	time.Sleep(50 * time.Millisecond) // let worker drain
}
