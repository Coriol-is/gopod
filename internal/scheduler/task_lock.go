package scheduler

import (
	"context"
	"sync"

	"github.com/Coriol-is/gopod/internal/store"
)

var taskStoreLocks sync.Map

// LockTasks serializes task mutation with snapshot refresh/enqueue/advancement.
// An already queued occurrence may finish; pause/cancel prevents future dispatch.
func LockTasks(st *store.Store) func() {
	actual, _ := taskStoreLocks.LoadOrStore(st, new(sync.Mutex))
	mu := actual.(*sync.Mutex)
	mu.Lock()
	return mu.Unlock
}

type taskLockKey struct{}

func taskOperationLock(ctx context.Context, st *store.Store) func() {
	if ctx.Value(taskLockKey{}) == st {
		return func() {}
	}
	return LockTasks(st)
}

// WithTaskLock runs fn holding the task lock. Unlike LockTasks it honors the
// lock key the IPC path puts in its context, so a caller already inside
// ProcessTaskRequest (which holds the non-reentrant mutex) does not deadlock.
// fn's context carries the key for anything it calls in turn.
func WithTaskLock(ctx context.Context, st *store.Store, fn func(ctx context.Context) error) error {
	release := taskOperationLock(ctx, st)
	defer release()
	return fn(context.WithValue(ctx, taskLockKey{}, st))
}
