package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/telegram"
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
	resolve := func(folder string) (string, bool) {
		c, err := st.GetByFolder(context.Background(), folder)
		if err != nil {
			return "", false
		}
		return c.JID, true
	}
	caps := ipc.NewCapabilities(ipc.OutputRootPolicy{DataDir: cfg.DataDir, ChatsDir: cfg.ChatsDir}, nil, resolve)
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

// preparedDelivery builds the CP-003 PR 5 wiring for the personal chat: oversized
// code documents and the automatic voice clip are prepared as immutable
// snapshots before upload. It exists only behind the artifact flag (its caller
// has opened the blob store) and only for the configured owner chat; a missing
// owner chat leaves the legacy paths. The speech grant names exactly the
// owner's chat, the one OpenAI backend, the personal credential reference and
// the 4000-character bound of the existing call. BilledCostAccepted records
// that enabling the artifact flag keeps today's billed personal voice, with no
// other backend and no fallback.
func preparedDelivery(cfg config.Config, st *store.Store, blobs *artifactfs.Blobs, log *slog.Logger) *telegram.PreparedDelivery {
	if cfg.OwnerChatID <= 0 {
		return nil
	}
	lim := st.ArtifactLimits()
	return &telegram.PreparedDelivery{
		Scopes: st, Artifacts: st, Blobs: blobs, Uploads: st, MaxFileBytes: lim.MaxFileBytes,
		Speech: &telegram.SpeechPreparer{
			Store: st, Blobs: blobs, Backend: telegram.NewOpenAISpeechBackend(), Credentials: telegram.NewEnvSpeechCredentials(),
			Policy: telegram.SpeechPolicy{Grants: []telegram.SpeechGrant{{
				JID:                fmt.Sprintf("tg:%d", cfg.OwnerChatID),
				Backend:            telegram.SpeechBackendOpenAI,
				CredentialRef:      telegram.PersonalSpeechCredential,
				MaxChars:           4000,
				BilledCostAccepted: true,
			}}},
			MaxFileBytes: lim.MaxFileBytes,
			Log:          log,
		},
	}
}

// generationGrace is how long a paid call may stay in progress before the live
// sweep records its outcome as unknown. It is longer than any backend timeout.
const generationGrace = 10 * time.Minute

// maintainGenerations records unknown outcomes. At startup nothing in this
// process can still be calling a backend (the outbox lock is held), so every
// in-progress row is swept at once; afterwards only rows older than the grace
// period. reconcile, when non-nil, also runs artifact reconciliation on the
// same cadence (set only when the IPC watcher does not already do it).
func maintainGenerations(ctx context.Context, st *store.Store, every time.Duration, reconcile func(context.Context), log *slog.Logger) {
	sweep := func(grace time.Duration) {
		if n, err := st.SweepGenerations(ctx, grace); err != nil {
			log.Warn("sweep generated-artifact ledger", slog.Any("err", err))
		} else if n > 0 {
			log.Warn("generated-artifact outcomes recorded as unknown", slog.Int("count", n))
		}
	}
	sweep(0)
	if reconcile != nil {
		reconcile(ctx)
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			sweep(generationGrace)
			if reconcile != nil {
				reconcile(ctx)
			}
		}
	}
}
