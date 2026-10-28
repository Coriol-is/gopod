package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
)

// durableIPC decides whether the durable text IPC route is active. It needs the
// route flag, a dispatcher that actually started (otherwise queued messages
// would never leave), a bound owner folder and the owner's personal chat. Any
// missing piece keeps the legacy direct send.
func durableIPC(routeFlag, dispatcherRunning bool, st *store.Store, ownerFolder string, ownerChatID int64) ipc.DurableConfig {
	if !routeFlag || !dispatcherRunning || st == nil || ownerFolder == "" || ownerChatID <= 0 {
		return ipc.DurableConfig{}
	}
	return ipc.DurableConfig{Store: st, PersonalJID: fmt.Sprintf("tg:%d", ownerChatID)}
}

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
