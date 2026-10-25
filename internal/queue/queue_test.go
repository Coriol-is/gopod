package queue

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strconv"
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

// TestCloseInterruptsTurnWhoseHandlerReturnsNil covers the production
// Telegram handler, which swallows cancellation and returns nil: the
// turn must still be marked interrupted, never done.
func TestCloseInterruptsTurnWhoseHandlerReturnsNil(t *testing.T) {
	fs := newFakeStore()
	started := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		close(started)
		<-ctx.Done()
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := New(ctx, handler, 1, fs, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	<-started
	cancel()
	q.Close()
	calls := fs.snapshot()
	if !contains(calls, "finished:1:interrupted") {
		t.Errorf("calls = %v, want finished:1:interrupted", calls)
	}
	if contains(calls, "finished:1:done") {
		t.Errorf("calls = %v, must not contain finished:1:done", calls)
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

func TestRecoverReenqueuesUnfinishedAsResumed(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Attempts: 1, Text: "running one"},
		{ID: 2, Folder: "b", Source: "telegram", SourceID: "2", Attempts: 0, Text: "pending one"},
	}
	got := make(chan Item, 2)
	handler := func(ctx context.Context, items []Item) error {
		for _, it := range items {
			got <- it
		}
		return nil
	}
	q := New(context.Background(), handler, 2, fs, silentLog())
	n, err := q.Recover(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Recover = (%d, %v), want (2, nil)", n, err)
	}
	seen := map[int64]Item{}
	for i := 0; i < 2; i++ {
		select {
		case it := <-got:
			seen[it.ID] = it
		case <-time.After(2 * time.Second):
			t.Fatal("timeout")
		}
	}
	if !seen[1].Resumed {
		t.Error("turn 1 (was running) not marked Resumed")
	}
	if seen[2].Resumed {
		t.Error("turn 2 (was pending) wrongly marked Resumed")
	}
	if contains(fs.snapshot(), "insert:1") || contains(fs.snapshot(), "insert:2") {
		t.Error("Recover must not re-insert rows")
	}
}

func TestRecoverPreservesOrderPerChat(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Attempts: 1},
		{ID: 2, Folder: "a", Source: "telegram", SourceID: "2"},
	}
	var order []int64
	var mu sync.Mutex
	done := make(chan struct{})
	var once sync.Once
	handler := func(ctx context.Context, items []Item) error {
		mu.Lock()
		for _, it := range items {
			order = append(order, it.ID)
		}
		if len(order) == 2 {
			once.Do(func() { close(done) })
		}
		mu.Unlock()
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	n, err := q.Recover(context.Background())
	if err != nil || n != 2 {
		t.Fatalf("Recover = (%d, %v), want (2, nil)", n, err)
	}
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for both items")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(order) != 2 || order[0] != 1 || order[1] != 2 {
		t.Errorf("order = %v, want [1 2]", order)
	}
}

func TestRecoverAfterCloseCountsNothing(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{{ID: 5, Folder: "a", Source: "telegram", SourceID: "5", Attempts: 1}}
	handler := func(ctx context.Context, items []Item) error {
		t.Error("handler must not run after Close")
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	q.Close()
	n, err := q.Recover(context.Background())
	if err != nil || n != 0 {
		t.Errorf("Recover = (%d, %v), want (0, nil)", n, err)
	}
	for _, c := range fs.snapshot() {
		if strings.HasPrefix(c, "running:") {
			t.Errorf("unexpected %q after Close", c)
		}
	}
}

func TestRecoverCrashLoopIsFailedWithoutRunning(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 9, Folder: "a", Source: "telegram", SourceID: "9", Attempts: MaxAttempts},
	}
	handler := func(ctx context.Context, items []Item) error {
		t.Error("handler must not run for a crash-looping turn")
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	var hookErr error
	q.OnDone(func(it Item, err error) { hookErr = err })
	n, err := q.Recover(context.Background())
	if err != nil {
		t.Fatalf("Recover: %v", err)
	}
	if n != 0 {
		t.Errorf("Recover re-enqueued %d, want 0", n)
	}
	if !contains(fs.snapshot(), "finished:9:failed") {
		t.Errorf("calls = %v, want finished:9:failed", fs.snapshot())
	}
	if hookErr == nil || !errors.Is(hookErr, ErrCrashLoop) {
		t.Errorf("hook err = %v, want ErrCrashLoop", hookErr)
	}
}

func TestRecoverWithoutStoreIsNoop(t *testing.T) {
	q := New(context.Background(), func(context.Context, []Item) error { return nil }, 1, nil, silentLog())
	if n, err := q.Recover(context.Background()); n != 0 || err != nil {
		t.Errorf("Recover = (%d, %v), want (0, nil)", n, err)
	}
}

// statusStore is a TurnStore that keeps each row's status and, like
// store.ListUnfinishedTurns, loads only pending/running/interrupted.
type statusStore struct {
	mu     sync.Mutex
	nextID int64
	rows   map[int64]*Item
	status map[int64]string
}

func newStatusStore() *statusStore {
	return &statusStore{rows: map[int64]*Item{}, status: map[int64]string{}}
}

func (s *statusStore) InsertTurn(_ context.Context, it Item) (int64, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	it.ID = s.nextID
	s.rows[it.ID] = &it
	s.status[it.ID] = "pending"
	return it.ID, false, nil
}

func (s *statusStore) MarkRunning(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[id] = "running"
	s.rows[id].Attempts++
	return nil
}

func (s *statusStore) MarkFinished(_ context.Context, id int64, status, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status[id] = status
	return nil
}

func (s *statusStore) LoadUnfinished(_ context.Context) ([]Item, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Item
	for id := int64(1); id <= s.nextID; id++ {
		switch s.status[id] {
		case "pending", "running", StatusInterrupted:
			out = append(out, *s.rows[id])
		}
	}
	return out, nil
}

func (s *statusStore) statusOf(id int64) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status[id]
}

// TestHandlerErrStoppedMarksStopped: a handler reporting ErrStopped
// ends the turn as stopped, without a retry, and the chat keeps going.
func TestHandlerErrStoppedMarksStopped(t *testing.T) {
	ss := newStatusStore()
	var calls int32
	handler := func(ctx context.Context, items []Item) error {
		if atomic.AddInt32(&calls, 1) == 1 {
			return fmt.Errorf("streaming: %w", ErrStopped)
		}
		return nil
	}
	q := New(context.Background(), handler, 1, ss, silentLog())
	q.BackoffFunc = func(int) time.Duration { return time.Millisecond }
	finished := make(chan error, 2)
	q.OnDone(func(_ Item, err error) { finished <- err })

	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	select {
	case err := <-finished:
		if err != nil {
			t.Errorf("OnDone err = %v, want nil for a stopped turn", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for stopped turn")
	}
	if got := ss.statusOf(1); got != StatusStopped {
		t.Errorf("status = %q, want %q", got, StatusStopped)
	}

	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "2"})
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for next turn")
	}
	if got := atomic.LoadInt32(&calls); got != 2 {
		t.Errorf("handler calls = %d, want 2 (stopped turn must not be retried)", got)
	}
	if got := ss.statusOf(2); got != StatusDone {
		t.Errorf("next turn status = %q, want done", got)
	}
}

// TestQueueRecoverIgnoresStopped: after a stop, a restart's Recover
// does not replay the turn.
func TestQueueRecoverIgnoresStopped(t *testing.T) {
	ss := newStatusStore()
	stopped := make(chan struct{})
	q := New(context.Background(), func(context.Context, []Item) error {
		return ErrStopped
	}, 1, ss, silentLog())
	q.OnDone(func(Item, error) { close(stopped) })
	q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1"})
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
	q.Close()

	var replays int32
	q2 := New(context.Background(), func(context.Context, []Item) error {
		atomic.AddInt32(&replays, 1)
		return nil
	}, 1, ss, silentLog())
	defer q2.Close()
	n, err := q2.Recover(context.Background())
	if err != nil || n != 0 {
		t.Fatalf("Recover = (%d, %v), want (0, nil)", n, err)
	}
	time.Sleep(20 * time.Millisecond)
	if atomic.LoadInt32(&replays) != 0 {
		t.Error("stopped turn was replayed")
	}
}

func TestRecoverKeepsThreadID(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Attempts: 1, ThreadID: 9}}
	got := make(chan Item, 1)
	handler := func(ctx context.Context, items []Item) error {
		got <- items[0]
		return nil
	}
	q := New(context.Background(), handler, 1, fs, silentLog())
	if n, err := q.Recover(context.Background()); err != nil || n != 1 {
		t.Fatalf("Recover = (%d, %v), want (1, nil)", n, err)
	}
	select {
	case it := <-got:
		if it.ThreadID != 9 {
			t.Errorf("recovered ThreadID = %d, want 9", it.ThreadID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timeout")
	}
}

// batchRecorder is a handler that records each call's items and can hold
// the first call open until released.
type batchRecorder struct {
	mu       sync.Mutex
	batches  [][]Item
	cur, max int32
	first    chan struct{} // closed when the first call has started
	release  chan struct{} // first call waits on this (nil = no gate)
	once     sync.Once
	total    int
	done     chan struct{}
	want     int
}

func newBatchRecorder(want int, gate bool) *batchRecorder {
	r := &batchRecorder{first: make(chan struct{}), done: make(chan struct{}), want: want}
	if gate {
		r.release = make(chan struct{})
	}
	return r
}

func (r *batchRecorder) handle(ctx context.Context, items []Item) error {
	n := atomic.AddInt32(&r.cur, 1)
	for {
		old := atomic.LoadInt32(&r.max)
		if n <= old || atomic.CompareAndSwapInt32(&r.max, old, n) {
			break
		}
	}
	r.mu.Lock()
	first := len(r.batches) == 0
	r.batches = append(r.batches, append([]Item(nil), items...))
	r.mu.Unlock()
	if first {
		close(r.first)
		if r.release != nil {
			<-r.release
		}
	}
	atomic.AddInt32(&r.cur, -1)
	r.mu.Lock()
	r.total += len(items)
	if r.total >= r.want {
		r.once.Do(func() { close(r.done) })
	}
	r.mu.Unlock()
	return nil
}

func (r *batchRecorder) wait(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for items")
	}
}

// shape renders batches as "0|5,5|0" using item Text, asserting each
// batch holds a single ThreadID.
func (r *batchRecorder) shape(t *testing.T) string {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	var parts []string
	for _, b := range r.batches {
		var txt []string
		for _, it := range b {
			if it.ThreadID != b[0].ThreadID {
				t.Errorf("batch mixes thread ids: %+v", b)
			}
			txt = append(txt, it.Text)
		}
		parts = append(parts, strings.Join(txt, ","))
	}
	return strings.Join(parts, "|")
}

// Main-chat and thread messages of one chat share a folder, so the queue
// still serializes and orders them per chat, but never in one batch.
func TestQueueOrdersMainAndThreadMessagesPerChat(t *testing.T) {
	r := newBatchRecorder(4, true)
	q := New(context.Background(), r.handle, 4, nil, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "alice", ThreadID: 0, Text: "0"})
	<-r.first
	for i, th := range []int{5, 0, 6} {
		q.Enqueue(context.Background(), Item{Folder: "alice", ThreadID: th, Text: strconv.Itoa(i + 1)})
	}
	close(r.release)
	r.wait(t)
	if got := r.shape(t); got != "0|1|2|3" {
		t.Errorf("batches = %s, want 0|1|2|3", got)
	}
	if mc := atomic.LoadInt32(&r.max); mc > 1 {
		t.Errorf("max concurrent = %d, want 1", mc)
	}
}

func TestQueueBatchesLeadingSameThreadRun(t *testing.T) {
	r := newBatchRecorder(5, true)
	q := New(context.Background(), r.handle, 4, nil, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "alice", Text: "x"})
	<-r.first // a turn is running; the rest queue up behind it
	for i, th := range []int{0, 5, 5, 0} {
		q.Enqueue(context.Background(), Item{Folder: "alice", ThreadID: th, Text: strconv.Itoa(i)})
	}
	close(r.release)
	r.wait(t)
	if got := r.shape(t); got != "x|0|1,2|3" {
		t.Errorf("batches = %s, want x|0|1,2|3", got)
	}
}

func TestRecoverSplitsThreadsIntoSeparateBatches(t *testing.T) {
	fs := newFakeStore()
	fs.unfinished = []Item{
		{ID: 1, Folder: "a", Source: "telegram", SourceID: "1", Text: "1", ThreadID: 7, Attempts: 1, PlaceholderMsgID: 99},
		{ID: 2, Folder: "a", Source: "telegram", SourceID: "2", Text: "2"},
	}
	r := newBatchRecorder(2, false)
	q := New(context.Background(), r.handle, 1, fs, silentLog())
	if n, err := q.Recover(context.Background()); err != nil || n != 2 {
		t.Fatalf("Recover = (%d, %v), want (2, nil)", n, err)
	}
	r.wait(t)
	if got := r.shape(t); got != "1|2" {
		t.Errorf("batches = %s, want 1|2", got)
	}
}

// Stage 1 of the agent acquire order (queue slot -> chat agent slot ->
// agent gate, see runner/gate.go): a worker waiting for a queue slot
// leaves on shutdown without running, while the slot holder (blocked in
// its handler, e.g. waiting for the agent gate) is interrupted.
func TestCloseWhileWaitingForQueueSlot(t *testing.T) {
	started := make(chan string, 2)
	handler := func(ctx context.Context, items []Item) error {
		started <- items[0].Folder
		<-ctx.Done() // like a turn waiting for the agent gate
		return ctx.Err()
	}
	q := New(context.Background(), handler, 1, nil, silentLog())
	q.Enqueue(context.Background(), Item{Folder: "a"})
	if got := <-started; got != "a" {
		t.Fatalf("first handler for %q", got)
	}
	q.Enqueue(context.Background(), Item{Folder: "b"}) // waits for the slot

	closed := make(chan struct{})
	go func() { q.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(2 * time.Second):
		t.Fatal("Close hung with a worker waiting for a queue slot")
	}
	select {
	case f := <-started:
		t.Fatalf("handler for %q ran after Close", f)
	default:
	}
}

func TestQueuePosition_Waiting(t *testing.T) {
	started := make(chan int64, 8)
	release := make(chan struct{})
	handler := func(ctx context.Context, items []Item) error {
		started <- items[0].ID
		<-release
		return nil
	}
	q := New(context.Background(), handler, 10, nil, silentLog())
	defer q.Close()
	ctx := context.Background()

	q.Enqueue(ctx, Item{ID: 1, Folder: "alice", ThreadID: 1})
	if id := <-started; id != 1 {
		t.Fatalf("first batch = turn %d", id)
	}
	// Waiting behind turn 1: a batch holds one thread's consecutive
	// items, so 2 and 3 run together, then 4, then 5.
	q.Enqueue(ctx, Item{ID: 2, Folder: "alice", ThreadID: 2})
	q.Enqueue(ctx, Item{ID: 3, Folder: "alice", ThreadID: 2})
	q.Enqueue(ctx, Item{ID: 4, Folder: "alice", ThreadID: 1})
	q.Enqueue(ctx, Item{ID: 5, Folder: "alice", ThreadID: 3})

	for _, c := range []struct {
		folder string
		id     int64
		want   int
	}{
		{"alice", 1, 0}, // running
		{"alice", 2, 1},
		{"alice", 3, 1},
		{"alice", 4, 2},
		{"alice", 5, 3},
		{"alice", 99, 0}, // unknown
		{"alice", 0, 0},  // no id
		{"bob", 2, 0},    // another chat
	} {
		if got := q.Position(c.folder, c.id); got != c.want {
			t.Errorf("Position(%s, %d) = %d, want %d", c.folder, c.id, got, c.want)
		}
	}

	release <- struct{}{} // turn 1 done; 2+3 start
	if id := <-started; id != 2 {
		t.Fatalf("second batch starts with turn %d", id)
	}
	if got := q.Position("alice", 3); got != 0 {
		t.Errorf("turn 3 runs in the batch of 2: position %d", got)
	}
	if got := q.Position("alice", 4); got != 1 {
		t.Errorf("turn 4 is next: position %d", got)
	}
	if got := q.Position("alice", 5); got != 2 {
		t.Errorf("turn 5: position %d", got)
	}
	close(release)
}
