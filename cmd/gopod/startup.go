package main

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

type startupState interface {
	ListRegistered(context.Context) ([]store.RegisteredChat, error)
	GetState(context.Context, string) (string, error)
}
type startupAgent interface {
	RestoreChatProvider(string, runner.AgentProvider)
	BlockChatSelection(string)
	SetChatModel(string, string)
	StartupCheck(context.Context, string, runner.Tier, *mountsec.Allowlist) runner.StartupReport
}

// Restore all choices before warming only the configured owner's registered
// workspace. Other chats stay lazy; a boot must not start every historical agent.
func restoreAndWarmOwner(ctx context.Context, st startupState, agent startupAgent, ownerID int64, providers map[string]runner.AgentProvider, al *mountsec.Allowlist, log *slog.Logger) error {
	chats, err := st.ListRegistered(ctx)
	if err != nil {
		log.Warn("agent startup check skipped", slog.String("reason", "registration_unavailable"))
		return fmt.Errorf("agent startup: cannot restore registered chat selections")
	}
	ownerFolder := ""
	ownerRestoreOK := false
	for _, chat := range chats {
		restored := true
		saved, err := st.GetState(ctx, "provider:"+chat.Folder)
		if err != nil {
			restored = false
		} else if saved != "" {
			if p, ok := providers[saved]; ok {
				agent.RestoreChatProvider(chat.Folder, p)
			} else {
				restored = false
			}
		}
		model, err := st.GetState(ctx, "model:"+chat.Folder)
		if err != nil {
			restored = false
		} else {
			agent.SetChatModel(chat.Folder, model)
		}
		if !restored {
			agent.BlockChatSelection(chat.Folder)
			log.Warn("agent selection restore degraded", slog.String("reason", "selection_unavailable"))
		}
		if ownerID != 0 && chat.JID == fmt.Sprintf("tg:%d", ownerID) {
			ownerFolder = chat.Folder
			ownerRestoreOK = restored
		}
	}
	if ownerFolder == "" {
		log.Info("agent startup check skipped", slog.String("reason", "owner_not_registered"))
		return nil
	}
	if !ownerRestoreOK {
		log.Warn("agent startup check skipped", slog.String("reason", "owner_selection_unavailable"))
		return nil
	}
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	report := agent.StartupCheck(checkCtx, ownerFolder, runner.TierOwner, al)
	cancel()
	modelLabel := report.Model
	if modelLabel == "" {
		modelLabel = "provider default"
	}
	log.Info("agent startup self-check", slog.String("provider", report.Provider), slog.String("model", modelLabel), slog.String("container", report.Container), slog.String("cli", report.CLI), slog.String("auth", report.Auth), slog.String("cli_version", report.Version))
	// Management probes never create an agent conversation. Only invoke after
	// local readiness; failed auth must not turn into a network/login retry loop.
	if management, ok := agent.(interface {
		ManagementCheck(context.Context, string) runner.ManagementReport
	}); ok && report.Container == "ready" && report.CLI == "ready" && report.Auth == "authenticated" {
		remoteCtx, remoteCancel := context.WithTimeout(ctx, 10*time.Second)
		observation := management.ManagementCheck(remoteCtx, ownerFolder)
		remoteCancel()
		log.Info("provider management self-check", slog.Time("checked_at", observation.CheckedAt), slog.String("provider", observation.Provider), slog.String("auth", observation.Auth), slog.String("quota", observation.Quota), slog.String("quota_scope", observation.QuotaScope), slog.String("error_class", observation.ErrorClass), slog.String("inference", observation.Inference), slog.String("ordinary_usage_allowed", startupBool(observation.OrdinaryUsageAllowed)), slog.String("spend_control_reached", startupBool(observation.SpendControlReached)))
		for _, w := range []struct {
			name  string
			value *runner.ManagementWindow
		}{{"primary", observation.Primary}, {"secondary", observation.Secondary}} {
			if w.value == nil {
				continue
			}
			used := "unknown"
			if w.value.UsedPercent != nil {
				used = fmt.Sprint(*w.value.UsedPercent)
			}
			reset := "unknown"
			if w.value.ResetsAt != nil {
				reset = fmt.Sprint(*w.value.ResetsAt)
			}
			log.Info("provider quota window", slog.String("provider", observation.Provider), slog.String("scope", observation.QuotaScope), slog.String("window", w.name), slog.String("used_percent", used), slog.String("resets_at", reset))
		}

	}
	// Local credentials, a quota snapshot or a catalog entry are not an inference guarantee.
	return nil
}

func startupBool(v *bool) string {
	if v == nil {
		return "unknown"
	}
	if *v {
		return "true"
	}
	return "false"
}
