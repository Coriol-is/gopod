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
	return ipc.DurableConfig{PrepareText: telegram.PrepareIPCText, Store: st, PersonalJID: fmt.Sprintf("tg:%d", ownerChatID)}
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
// capability registry for CP-003. The runner issues capabilities from this
// registry through artifactIssuer, but only for the owner's private chat. The
// flag must not be enabled before the matched backup and restore gate in D039
// passes.
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

// sweepGenerationsAtStartup records an unknown outcome for every ledger row an
// earlier process left in progress. It must run synchronously before the bot
// polls or the scheduler starts, with cutoff = this process's start, so it can
// never flip a generation this process began.
func sweepGenerationsAtStartup(ctx context.Context, st *store.Store, processStart time.Time, log *slog.Logger) {
	n, err := st.SweepGenerationsBefore(ctx, processStart)
	switch {
	case err != nil:
		log.Warn("startup sweep of the generated-artifact ledger", slog.Any("err", err))
	case n > 0:
		log.Warn("generated-artifact outcomes recorded as unknown", slog.Int("count", n))
	}
}

// maintainGenerations is the periodic part: a live sweep (rows older than the
// grace period), the retention purge of finished rows and, when non-nil,
// artifact reconciliation (set only when the IPC watcher does not do it).
func maintainGenerations(ctx context.Context, st *store.Store, every, retention time.Duration, reconcile func(context.Context), log *slog.Logger) {
	if retention <= 0 {
		retention = store.DefaultGenerationRetention
	}
	tick := func() {
		if n, err := st.SweepGenerations(ctx, generationGrace); err != nil {
			log.Warn("sweep generated-artifact ledger", slog.Any("err", err))
		} else if n > 0 {
			log.Warn("generated-artifact outcomes recorded as unknown", slog.Int("count", n))
		}
		if n, err := st.PurgeGenerations(ctx, retention, 500); err != nil {
			log.Warn("purge generated-artifact ledger", slog.Any("err", err))
		} else if n > 0 {
			log.Info("generated-artifact ledger purged", slog.Int("rows", n))
		}
		if reconcile != nil {
			reconcile(ctx)
		}
	}
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
			tick()
		}
	}
}

// artifactIssuer builds the runner-side capability issuer (CP-003 PR C). It
// exists only when V1 artifact requests are actually admitted (the effective
// config from artifactIPC: flag on, dispatcher running, durable route on) and
// there is a private owner chat, and it shares the watcher's capability
// registry so that what it issues is what the watcher checks. Otherwise nil:
// no exec gets a capability and every V1 request stays stale_capability.
func artifactIssuer(cfg config.Config, ac ipc.ArtifactConfig, st *store.Store, log *slog.Logger) *ipc.RunIssuer {
	if ac.Store == nil || ac.Blobs == nil || ac.Caps == nil || st == nil || cfg.OwnerChatID <= 0 {
		return nil
	}
	return &ipc.RunIssuer{
		Caps:     ac.Caps,
		Scopes:   st,
		OwnerJID: fmt.Sprintf("tg:%d", cfg.OwnerChatID),
		ChatsDir: cfg.ChatsDir,
		Resolve: func(folder string) (string, bool) {
			c, err := st.GetByFolder(context.Background(), folder)
			if err != nil {
				return "", false
			}
			return c.JID, true
		},
		Log: log,
	}
}
