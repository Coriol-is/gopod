package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

// recoveryQuarantine reports whether this start must be a quarantined one: the
// operator asked for it, or the restore latch exists in the data directory. It
// is evaluated before the store is opened or anything is started.
func recoveryQuarantine(cfg config.Config) (bool, string) {
	if active, info := maintenance.LatchActive(cfg.DataDir); active {
		return true, "restore latch present: " + info.Reason
	}
	if cfg.RecoveryQuarantine {
		return true, "GOPOD_RECOVERY_QUARANTINE is set"
	}
	return false, ""
}

// runQuarantine is the whole of a quarantined start: it reads the data
// directory's database read-only and without migrations to log a validation
// result, registers the backup commands on a router, and then waits for ctx.
// It opens no store (Store.Open migrates), no Telegram client or poller, no
// scheduler, IPC watcher, queue, runner/Docker client, model or auth warmup,
// metrics listener or delivery. Leaving quarantine is a host action:
// `gopod backup quarantine release --yes`, then a normal start.
func runQuarantine(ctx context.Context, cfg config.Config, reason string, log *slog.Logger) error {
	log.Warn("recovery quarantine: control plane only", slog.String("reason", reason),
		slog.String("data_dir", cfg.DataDir))
	router := control.New(log)
	control.RegisterBackup(router, backupDeps(cfg, log))

	problem := validateQuarantinedStore(ctx, cfg.StorePath)
	if problem != "" {
		log.Error("recovery quarantine: store failed validation", slog.String("problem", problem))
	} else {
		log.Info("recovery quarantine: store passed read-only validation")
	}
	log.Info("recovery quarantine: waiting; no polling, scheduler, IPC, queue, warmup or delivery is running",
		slog.Int("control_commands", len(router.List())))
	<-ctx.Done()
	return nil
}

// validateQuarantinedStore checks the database file without touching it. A
// missing file is reported, not created.
func validateQuarantinedStore(ctx context.Context, path string) string {
	_, err := store.InspectDB(ctx, path)
	if err != nil {
		return fmt.Sprintf("inspect: %v", err)
	}
	if err := store.CheckSchemaCompat(ctx, path); err != nil {
		return err.Error()
	}
	return ""
}
