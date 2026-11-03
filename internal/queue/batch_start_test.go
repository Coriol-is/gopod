package queue

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

type batchStartStore struct {
	*fakeStore
	batchMu sync.Mutex
	seen    [][]Item
	fail    bool
	called  chan struct{}
}

func (s *batchStartStore) StartBatch(_ context.Context, items []Item) ([]int, error) {
	s.batchMu.Lock()
	defer s.batchMu.Unlock()
	s.seen = append(s.seen, append([]Item(nil), items...))
	if s.called != nil {
		select {
		case s.called <- struct{}{}:
		default:
		}
	}
	if s.fail {
		return nil, errors.New("batch commit unavailable")
	}
	attempts := make([]int, len(items))
	for i, it := range items {
		attempts[i] = it.Attempts + 1
	}
	return attempts, nil
}

func TestBatchStartSuppliesCommittedAttemptsAcrossRetries(t *testing.T) {
	fs := &batchStartStore{fakeStore: newFakeStore()}
	done := make(chan error, 2)
	var seen [][]int
	q := New(t.Context(), func(_ context.Context, items []Item) error {
		var ns []int
		for _, it := range items {
			ns = append(ns, it.Attempts)
		}
		seen = append(seen, ns)
		if len(seen) == 1 {
			return errors.New("pre-execution refusal")
		}
		return nil
	}, 1, fs, nil)
	q.BackoffFunc = func(int) time.Duration { return 0 }
	q.OnDone(func(_ Item, err error) { done <- err })
	q.BeginDrain()
	q.Enqueue(t.Context(), Item{Folder: "owner", Source: "telegram", SourceID: "1"})
	q.Enqueue(t.Context(), Item{Folder: "owner", Source: "telegram", SourceID: "2"})
	q.CancelDrain()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("handler did not finish")
		}
	}
	q.Close()
	if !reflect.DeepEqual(seen, [][]int{{1, 1}, {2, 2}}) {
		t.Fatalf("handler attempts: %v", seen)
	}
}

func TestBatchStartFailureWaitsWithoutCompletionOrRewritingRows(t *testing.T) {
	fs := &batchStartStore{fakeStore: newFakeStore(), fail: true, called: make(chan struct{}, 1)}
	q := New(t.Context(), func(context.Context, []Item) error { t.Error("handler invoked after failed commit"); return nil }, 1, fs, nil)
	q.OnDone(func(Item, error) { t.Error("unstarted batch reported complete") })
	q.Enqueue(t.Context(), Item{Folder: "owner", Source: "telegram", SourceID: "1"})
	select {
	case <-fs.called:
	case <-time.After(3 * time.Second):
		t.Fatal("no start attempt")
	}
	q.Close()
	if got := fs.snapshot(); !reflect.DeepEqual(got, []string{"insert:1"}) {
		t.Fatalf("failed start rewrote rows: %v", got)
	}
}

func TestBatchStartRetryPreservesArrivalOrder(t *testing.T) {
	fs := &batchStartStore{fakeStore: newFakeStore(), fail: true, called: make(chan struct{}, 1)}
	done := make(chan Item, 2)
	q := New(t.Context(), func(_ context.Context, items []Item) error {
		if len(items) != 1 || items[0].Attempts != 1 {
			t.Errorf("invalid attempts/batch: %+v", items)
		}
		return nil
	}, 1, fs, nil)
	defer q.Close()
	q.BackoffFunc = func(int) time.Duration { return time.Millisecond }
	q.OnDone(func(it Item, err error) {
		if err != nil {
			t.Error(err)
		}
		done <- it
	})
	q.Enqueue(t.Context(), Item{Folder: "owner", Source: "telegram", SourceID: "1"})
	select {
	case <-fs.called:
	case <-time.After(3 * time.Second):
		t.Fatal("no start attempt")
	}
	q.Enqueue(t.Context(), Item{Folder: "owner", Source: "telegram", SourceID: "2"})
	select {
	case it := <-done:
		t.Fatalf("completed before commit: %+v", it)
	default:
	}
	fs.batchMu.Lock()
	fs.fail = false
	fs.batchMu.Unlock()
	for _, want := range []string{"1", "2"} {
		select {
		case it := <-done:
			if it.SourceID != want {
				t.Fatalf("out of order: %+v want %s", it, want)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("no completion")
		}
	}
}

func TestBatchStartDoesNotMutateRestartSnapshot(t *testing.T) {
	q := New(t.Context(), func(context.Context, []Item) error { return nil }, 1, nil, nil)
	defer q.Close()
	q.mu.Lock()
	q.chats["owner"] = &chatState{pending: []Item{{ID: 1, Folder: "owner"}}}
	q.mu.Unlock()
	items, _ := q.beginBatch(t.Context(), "owner")
	items[0].Attempts = 7 // handler-local committed counters must not alias the snapshot
	q.mu.Lock()
	got := q.running["owner"].items[0].Attempts
	q.mu.Unlock()
	if got != 0 {
		t.Fatalf("restart snapshot mutated: %d", got)
	}
	q.endBatch("owner")
}
