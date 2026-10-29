package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/config"
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

// openArtifactStore opens the host-private snapshot store and the per-run
// capability registry for CP-003. Nothing in this binary issues a capability
// yet (the runner integration is a later slice), so with the flag on every V1
// request is still rejected as stale_capability; the flag only proves the
// storage, reconcile and upload plumbing. It must not be enabled before the
// matched backup and restore gate in D039 passes.
func openArtifactStore(cfg config.Config, st *store.Store) (ipc.ArtifactConfig, *artifactfs.Blobs, error) {
	blobs, err := artifactfs.OpenBlobs(filepath.Join(cfg.DataDir, "artifacts"))
	if err != nil {
		return ipc.ArtifactConfig{}, nil, fmt.Errorf("open artifact store: %w", err)
	}
	caps := ipc.NewCapabilities(ipc.OutputRootPolicy{DataDir: cfg.DataDir, ChatsDir: cfg.ChatsDir}, nil)
	return ipc.ArtifactConfig{Store: st, Blobs: blobs, Caps: caps}, blobs, nil
}

// artifactIPC enables V1 artifact requests only when the durable text route is
// active; otherwise V1 requests keep the legacy path.
func artifactIPC(durable ipc.DurableConfig, artifacts ipc.ArtifactConfig) ipc.ArtifactConfig {
	if durable.Store == nil {
		return ipc.ArtifactConfig{}
	}
	return artifacts
}
