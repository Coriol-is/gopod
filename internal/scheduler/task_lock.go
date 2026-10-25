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
