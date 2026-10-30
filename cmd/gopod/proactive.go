package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/proactive"
	"github.com/Coriol-is/gopod/internal/store"
)

// proactiveService resolves the preset service for the owner chat on each call,
// so /proactive starts working as soon as the owner chat is registered, with no
// restart. It never guesses a folder: no registration means unavailable.
func proactiveService(st *store.Store, ownerChatID int64) func(context.Context) (*proactive.Service, error) {
	jid := fmt.Sprintf("tg:%d", ownerChatID)
	return func(ctx context.Context) (*proactive.Service, error) {
		if ownerChatID == 0 {
			return nil, fmt.Errorf("owner chat id is not configured")
		}
		rc, err := st.GetRegistered(ctx, jid)
		if err != nil {
			return nil, fmt.Errorf("owner chat %s: %w", jid, err)
		}
		return proactive.New(proactive.Config{Store: st, Folder: rc.Folder, ChatJID: jid})
	}
}

// upgradeProactiveTemplates brings preset tasks up to the catalog's template
// version at startup (section 6.6). Presets are off by default, so with no
// preset enabled this changes nothing. A failure is logged, never fatal.
func upgradeProactiveTemplates(ctx context.Context, log *slog.Logger, resolve func(context.Context) (*proactive.Service, error)) {
	svc, err := resolve(ctx)
	if err != nil {
		log.Debug("proactive template upgrade skipped", slog.String("reason", err.Error()))
		return
	}
	res, err := svc.UpgradeTemplates(ctx)
	if err != nil {
		log.Warn("proactive template upgrade failed", slog.String("err", err.Error()))
		return
	}
	for _, r := range res {
		log.Info("proactive template upgrade", slog.String("preset", r.PresetID), slog.String("outcome", string(r.Outcome)))
	}
}
