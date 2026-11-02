package queue

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

type completedCommitFailure struct{}

func (completedCommitFailure) Error() string  { return "result commit failed after execution" }
func (completedCommitFailure) NoReplay() bool { return true }

func TestCompletedCommitFailureNeverRetriesOrRecovers(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		t.Run(fmt.Sprint("shutdown=", shutdown), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			st := newStatusStore()
			var calls atomic.Int32
			q := New(ctx, func(context.Context, []Item) error {
				calls.Add(1)
				if shutdown {
					cancel()
				}
				return fmt.Errorf("wrapped: %w", completedCommitFailure{})
			}, 1, st, silentLog())
			q.BackoffFunc = func(int) time.Duration { return 0 }
			done := make(chan error, 1)
			q.OnDone(func(_ Item, err error) { done <- err })
			id, _ := q.Enqueue(context.Background(), Item{Folder: "a", Source: "telegram", SourceID: "1:1"})
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("failure hidden")
				}
			case <-time.After(5 * time.Second):
				t.Fatal("timeout")
			}
			q.Close()
			if calls.Load() != 1 || st.statusOf(id) != StatusFailed {
				t.Fatalf("calls=%d status=%s", calls.Load(), st.statusOf(id))
			}
			q2 := New(context.Background(), func(context.Context, []Item) error { calls.Add(1); return nil }, 1, st, silentLog())
			if n, err := q2.Recover(context.Background()); err != nil || n != 0 {
				t.Fatalf("recovery scheduled completed execution: n=%d err=%v", n, err)
			}
			q2.Close()
			if calls.Load() != 1 {
				t.Fatal("completed execution replayed after restart")
			}
		})
	}
}
