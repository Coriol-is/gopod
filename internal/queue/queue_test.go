package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
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

	q := New(context.Background(), handler, 10, nil, silentLog())
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

	q := New(context.Background(), handler, 2, nil, silentLog())
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

	q := New(context.Background(), handler, 10, nil, silentLog())
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

	q := New(context.Background(), handler, 10, nil, silentLog())
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

	q := New(context.Background(), handler, 10, nil, silentLog())
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

// fakeStore records TurnStore calls in order so tests can assert
// ordering relative to handler invocations.
type fakeStore struct {
	mu         sync.Mutex
	nextID     int64
	calls      []string // "insert:<sid>", "running:<id>", "finished:<id>:<status>"
	dupIDs     map[string]bool
	insertErr  error
	unfinished []Item
}

func newFakeStore() *fakeStore { return &fakeStore{dupIDs: map[string]bool{}} }

func (f *fakeStore) InsertTurn(_ context.Context, it Item) (int64, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.insertErr != nil {
		return 0, false, f.insertErr
	}
	key := it.Source + ":" + it.SourceID
	if f.dupIDs[key] {
		return 0, true, nil
	}
	f.dupIDs[key] = true
	f.nextID++
	f.calls = append(f.calls, "insert:"+it.SourceID)
	return f.nextID, false, nil
}

func (f *fakeStore) MarkRunning(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("running:%d", id))
	return nil
}

func (f *fakeStore) MarkFinished(_ context.Context, id int64, status, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, fmt.Sprintf("finished:%d:%s", id, status))
	return nil
}

func (f *fakeStore) LoadUnfinished(_ context.Context) ([]Item, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Item(nil), f.unfinished...), nil
}

func (f *fakeStore) snapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func TestEnqueuePersistsBeforeRunAndFinishesAfter(t *testing.T) {
	fs := newFakeStore()
	done := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		fs.mu.Lock()
		fs.calls = append(fs.calls, "handler")
		fs.mu.Unlock()
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.OnDone(func(it Item, err error) {
		if it.ID == 1 {
			close(done)
		}
	})
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1", Text: "x"})
	if id != 1 || dup {
		t.Fatalf("Enqueue = (%d, %v), want (1, false)", id, dup)
	}
	<-done
	want := []string{"insert:1", "running:1", "handler", "finished:1:done"}
	if got := fs.snapshot(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("calls = %v, want %v", got, want)
	}
}

func TestEnqueueDuplicateNeverReachesHandler(t *testing.T) {
	fs := newFakeStore()
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		atomic.AddInt32(&calls, int32(len(items)))
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	it := Item{Folder: "a", Source: "telegram", SourceID: "7", Text: "x"}
	q.Enqueue(context.Background(), it)
	_, dup := q.Enqueue(context.Background(), it)
	if !dup {
		t.Fatal("second Enqueue not reported as dup")
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&calls); n != 1 {
		t.Errorf("handler saw %d items, want 1", n)
	}
}

func TestEnqueueStoreErrorDrops(t *testing.T) {
	fs := newFakeStore()
	fs.insertErr = errors.New("disk full")
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	if id != 0 || dup {
		t.Errorf("Enqueue = (%d, %v), want (0, false)", id, dup)
	}
	time.Sleep(30 * time.Millisecond)
	if atomic.LoadInt32(&calls) != 0 {
		t.Error("handler ran despite insert failure")
	}
}

func TestCoalescedBatchMarksEveryItem(t *testing.T) {
	fs := newFakeStore()
	release := make(chan struct{})
	var batches int32
	handler := func(ctx context.Context, items []Item) error {
		if atomic.AddInt32(&batches, 1) == 1 {
			<-release // hold the first turn so the next two coalesce
		}
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	finished := make(chan int64, 3)
	q.OnDone(func(it Item, err error) { finished <- it.ID })
	for i := 1; i <= 3; i++ {
		q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: fmt.Sprint(i)})
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	got := map[int64]bool{}
	for i := 0; i < 3; i++ {
		select {
		case id := <-finished:
			got[id] = true
		case <-time.After(2 * time.Second):
			t.Fatal("timeout waiting for OnDone")
		}
	}
	if len(got) != 3 {
		t.Errorf("OnDone ids = %v, want 3 distinct", got)
	}
	calls := fs.snapshot()
	for _, id := range []string{"finished:2:done", "finished:3:done"} {
		if !contains(calls, id) {
			t.Errorf("missing %s in %v", id, calls)
		}
	}
}

func TestHandlerErrorMarksFailedAfterRetries(t *testing.T) {
	fs := newFakeStore()
	handler := func(ctx context.Context, items []Item) error { return errors.New("infra") }
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.BackoffFunc = func(int) time.Duration { return time.Millisecond }
	done := make(chan error, 1)
	q.OnDone(func(it Item, err error) { done <- err })
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	select {
	case err := <-done:
		if err == nil {
			t.Error("OnDone err = nil, want handler error")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout")
	}
	calls := fs.snapshot()
	if !contains(calls, "finished:1:failed") {
		t.Errorf("calls = %v, want finished:1:failed", calls)
	}
	if n := count(calls, "running:1"); n != DefaultMaxRetries+1 {
		t.Errorf("running marks = %d, want %d (attempts persisted per try)", n, DefaultMaxRetries+1)
	}
}

func TestCloseInterruptsInFlightTurn(t *testing.T) {
	fs := newFakeStore()
	started := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(ctx, handler, 1, fs, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	<-started
	cancel()
	q.Close()
	if calls := fs.snapshot(); !contains(calls, "finished:1:interrupted") {
		t.Errorf("calls = %v, want finished:1:interrupted", calls)
	}
}

func TestNilStoreKeepsMemoryOnlyBehaviour(t *testing.T) {
	done := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error { close(done); return nil }
	q := New(context.Background(), handler, 1, nil, silentLog())
	id, dup := q.Enqueue(context.Background(), Item{Folder: "a", Text: "x"})
	if id != 0 || dup {
		t.Errorf("Enqueue with nil store = (%d, %v), want (0, false)", id, dup)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler not called")
	}
}

func TestNoWorkStartedOnCancelledContext(t *testing.T) {
	fs := newFakeStore()
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		atomic.AddInt32(&calls, 1)
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q := New(ctx, handler, 1, fs, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	q.Close() // waits for the worker, so no sleeps needed
	if n := atomic.LoadInt32(&calls); n != 0 {
		t.Errorf("handler ran %d times on cancelled ctx", n)
	}
	for _, c := range fs.snapshot() {
		if strings.HasPrefix(c, "running:") || strings.HasPrefix(c, "finished:") {
			t.Errorf("unexpected store call %q", c)
		}
	}
	// Enqueue after Close leaves the row pending and starts nothing.
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "2"})
	// Turn 1 stays in memory (never drained); turn 2 is refused outright.
	if q.Pending() != 1 || q.ActiveChats() != 0 {
		t.Errorf("after Close: pending=%d active=%d, want 1, 0", q.Pending(), q.ActiveChats())
	}
}

func contains(ss []string, s string) bool { return count(ss, s) > 0 }

func count(ss []string, s string) int {
	n := 0
	for _, x := range ss {
		if x == s {
			n++
		}
	}
	return n
}
