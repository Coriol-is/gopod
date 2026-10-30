package queue

import (
	"context"
	"sync"
	"testing"
	"time"
)

// CP-033 section 3 asked whether a user message preempts a running scheduled
// task in the owner folder. It must not: the task finishes first, and a
// pending task and a pending user message never share a batch.
//
// The test is deterministic: the first task blocks on a channel until the other
// three items are confirmed pending (q.Pending is state, not time), and every
// handler checks on entry that no other handler is running.
func TestUserMessageNeverPreemptsOrBatchesWithARunningTask(t *testing.T) {
	var mu sync.Mutex
	var batches [][]string
	var running int
	var overlap, taskCancelled bool
	release := make(chan struct{})
	taskStarted := make(chan struct{})
	allDone := make(chan struct{})

	handler := func(ctx context.Context, items []Item) error {
		mu.Lock()
		running++
		if running > 1 {
			overlap = true
		}
		var kinds []string
		for _, it := range items {
			kinds = append(kinds, it.Source+":"+it.Text)
		}
		batches = append(batches, kinds)
		n := len(batches)
		mu.Unlock()

		if items[0].Text == "t1" {
			close(taskStarted)
			<-release
			if ctx.Err() != nil { // a preemption would have cancelled this context
				mu.Lock()
				taskCancelled = true
				mu.Unlock()
			}
		}
		mu.Lock()
		running--
		mu.Unlock()
		if n == 4 {
			close(allDone)
		}
		return nil
	}
	q := New(context.Background(), handler, 10, nil, silentLog())
	ctx := context.Background()

	q.Enqueue(ctx, Item{Folder: "owner", Source: "task", Text: "t1"})
	<-taskStarted
	q.Enqueue(ctx, Item{Folder: "owner", Source: "telegram", Text: "u1", IsOwner: true})
	q.Enqueue(ctx, Item{Folder: "owner", Source: "task", Text: "t2"})
	q.Enqueue(ctx, Item{Folder: "owner", Source: "telegram", Text: "u2", IsOwner: true})

	// All three are queued behind the running task; none has been handed to a handler.
	if got := q.Pending(); got != 3 {
		t.Fatalf("Pending = %d, want 3 queued behind the running task", got)
	}
	mu.Lock()
	started := len(batches)
	mu.Unlock()
	if started != 1 {
		t.Fatalf("%d handlers entered while the task was held, want 1", started)
	}
	close(release)

	select {
	case <-allDone:
	case <-time.After(5 * time.Second):
		mu.Lock()
		defer mu.Unlock()
		t.Fatalf("batches = %v", batches)
	}
	mu.Lock()
	defer mu.Unlock()
	if overlap {
		t.Fatal("two handlers ran at once in one folder")
	}
	if taskCancelled {
		t.Fatal("running task context was cancelled by a user message")
	}
	for _, b := range batches {
		sources := map[string]bool{}
		for _, k := range b {
			sources[k[:4]] = true
		}
		if len(sources) != 1 {
			t.Fatalf("a batch mixed a task and a user message: %v", batches)
		}
	}
}
