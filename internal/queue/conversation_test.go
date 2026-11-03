package queue

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

type delayedEnqueueStore struct {
	*statusStore
	committed, release chan struct{}
	failFinish         atomic.Bool
}

func (s *delayedEnqueueStore) InsertTurn(ctx context.Context, it Item) (int64, bool, error) {
	id, dup, err := s.statusStore.InsertTurn(ctx, it)
	if it.ThreadID == 7 && s.committed != nil {
		close(s.committed)
		<-s.release
	}
	return id, dup, err
}
func (s *delayedEnqueueStore) MarkFinished(ctx context.Context, id int64, status, reason string) error {
	if s.failFinish.Load() {
		return errors.New("write unavailable")
	}
	return s.statusStore.MarkFinished(ctx, id, status, reason)
}

func TestConversationQuiescenceJoinsCommittedLateEnqueue(t *testing.T) {
	st := &delayedEnqueueStore{statusStore: newStatusStore(), committed: make(chan struct{}), release: make(chan struct{})}
	var calls atomic.Int32
	q := New(t.Context(), func(context.Context, []Item) error { calls.Add(1); return nil }, 1, st, silentLog())
	defer q.Close()
	enqueued := make(chan int64, 1)
	go func() {
		id, _ := q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "late"})
		enqueued <- id
	}()
	<-st.committed
	q.BeginConversationQuiescence("owner", 7)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := q.WaitConversationQuiescent(ctx, "owner", 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("late accepted row escaped wait: %v", err)
	}
	close(st.release)
	id := <-enqueued
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	if err := q.WaitConversationQuiescent(ctx2, "owner", 7); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 || st.statusOf(id) != StatusStopped {
		t.Fatalf("late work ran or was lost: calls=%d status=%s", calls.Load(), st.statusOf(id))
	}
}
func TestConversationQuiescenceCancelsOnlyTargetAndPreservesSiblings(t *testing.T) {
	st := newStatusStore()
	entered := make(chan struct{})
	sibling := make(chan struct{}, 2)
	q := New(t.Context(), func(ctx context.Context, items []Item) error {
		if items[0].ThreadID == 7 {
			close(entered)
			<-ctx.Done()
			return ctx.Err()
		}
		sibling <- struct{}{}
		return nil
	}, 1, st, silentLog())
	defer q.Close()
	first, _ := q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "first"})
	<-entered
	pending, _ := q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "pending"})
	q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 8, Source: "telegram", SourceID: "sibling"})
	q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 0, Source: "telegram", SourceID: "main"})
	q.BeginConversationQuiescence("owner", 7)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := q.WaitConversationQuiescent(ctx, "owner", 7); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		select {
		case <-sibling:
		case <-ctx.Done():
			t.Fatal("sibling lane stalled")
		}
	}
	if st.statusOf(first) != StatusStopped || st.statusOf(pending) != StatusStopped {
		t.Fatal("target cancellation not settled")
	}
}
func TestConversationCancellationWriteFailureRetainsRowsAndHooks(t *testing.T) {
	st := &delayedEnqueueStore{statusStore: newStatusStore()}
	st.failFinish.Store(true)
	q := New(t.Context(), func(context.Context, []Item) error { t.Error("blocked handler ran"); return nil }, 1, st, silentLog())
	defer q.Close()
	q.BeginDrain()
	var hooks atomic.Int32
	q.OnDone(func(Item, error) { hooks.Add(1) })
	id, _ := q.Enqueue(t.Context(), Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "pending"})
	q.BeginConversationQuiescence("owner", 7)
	if err := q.WaitConversationQuiescent(t.Context(), "owner", 7); err == nil {
		t.Fatal("persistence failure hidden")
	}
	if q.Pending() != 1 || hooks.Load() != 0 || st.statusOf(id) == StatusStopped {
		t.Fatal("uncommitted cancellation discarded row or notified hook")
	}
	st.failFinish.Store(false)
	if err := q.WaitConversationQuiescent(t.Context(), "owner", 7); err != nil {
		t.Fatal(err)
	}
	if q.Pending() != 0 || hooks.Load() != 1 || st.statusOf(id) != StatusStopped {
		t.Fatal("retry did not settle exactly once")
	}
}
