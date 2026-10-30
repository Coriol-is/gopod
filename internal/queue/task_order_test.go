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
func TestUserMessageNeverPreemptsOrBatchesWithARunningTask(t *testing.T) {
	var mu sync.Mutex
	var batches [][]string
	release := make(chan struct{})
	taskStarted := make(chan struct{})
	var taskCtxErr error

	handler := func(ctx context.Context, items []Item) error {
		var kinds []string
		for _, it := range items {
			kinds = append(kinds, it.Source+":"+it.Text)
		}
		mu.Lock()
		batches = append(batches, kinds)
		mu.Unlock()
		if items[0].Text == "t1" {
			close(taskStarted)
			<-release
			taskCtxErr = ctx.Err() // a preemption would have cancelled this
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

	time.Sleep(100 * time.Millisecond) // the user message gets no slot while t1 runs
	mu.Lock()
	if len(batches) != 1 {
		mu.Unlock()
		t.Fatalf("a second turn started while the task was running: %v", batches)
	}
	mu.Unlock()
	close(release)

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(batches)
		mu.Unlock()
		if n >= 4 {
			break
		}
		select {
		case <-deadline:
			mu.Lock()
			defer mu.Unlock()
			t.Fatalf("batches = %v", batches)
		case <-time.After(10 * time.Millisecond):
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if taskCtxErr != nil {
		t.Fatalf("running task context was cancelled: %v", taskCtxErr)
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
