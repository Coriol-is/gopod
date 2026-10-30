package scheduler

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func TestWithTaskLockIsReentrantThroughTheContext(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	done := make(chan error, 1)
	go func() {
		done <- WithTaskLock(context.Background(), st, func(ctx context.Context) error {
			// The IPC path holds the lock and passes this context down; a second
			// WithTaskLock under it must not block on the non-reentrant mutex.
			return WithTaskLock(ctx, st, func(context.Context) error { return nil })
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("deadlock: nested WithTaskLock did not honor the context key")
	}
	// And it still excludes others once released.
	release := LockTasks(st)
	release()
}
