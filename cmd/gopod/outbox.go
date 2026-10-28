package main

import (
	"context"
	"errors"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// acquireOutboxOwner takes the exclusive outbox lock, retrying for up to wait
// when a previous instance is still shutting down. Any error other than the
// lock being held is returned at once.
func acquireOutboxOwner(ctx context.Context, st *store.Store, wait, poll time.Duration) (*store.OutboxOwner, error) {
	deadline := time.Now().Add(wait)
	for {
		owner, err := st.AcquireOutboxOwner(ctx)
		if !errors.Is(err, store.ErrOutboxLocked) {
			return owner, err
		}
		if time.Now().Add(poll).After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(poll):
		}
	}
}
