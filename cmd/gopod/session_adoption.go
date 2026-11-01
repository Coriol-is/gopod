package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/store"
)

// adoptSessionsIfRequested applies an owner's /sessions adopt request before
// anything else reads the session tables. A busy store (pending or running
// work) is not fatal: startup continues in legacy mode and the request stays.
func adoptSessionsIfRequested(ctx context.Context, st *store.Store, log *slog.Logger) error {
	mode, requested, err := st.SessionMappingMode(ctx)
	if err != nil {
		return fmt.Errorf("session adoption: %w", err)
	}
	if !requested || mode != store.SessionMappingLegacy {
		return nil
	}
	rep, err := st.AdoptLegacySessions(ctx)
	if errors.Is(err, store.ErrAdoptionBusy) {
		log.Warn("session adoption deferred: store has pending or running work; staying in legacy mode", slog.Any("err", err))
		return nil
	}
	if err != nil {
		return fmt.Errorf("session adoption: %w", err)
	}
	log.Info("legacy sessions adopted",
		slog.String("mode", rep.Mode),
		slog.Int("sessions", rep.Sessions),
		slog.Int("contexts", rep.Contexts),
		slog.Bool("already_done", rep.AlreadyDone))
	return nil
}
