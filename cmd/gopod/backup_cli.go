package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/runner"
)

// dockerLister reports live agent containers through the Docker SDK. It can
// only list: the backup fence has no way to stop or remove a container.
type dockerLister struct{ d *runner.Docker }

func (l dockerLister) LiveOwned(ctx context.Context) ([]string, error) {
	list, err := l.d.ListGopodContainers(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, c := range list {
		switch c.State {
		case "running", "restarting", "paused":
			ids = append(ids, c.ID)
		}
	}
	return ids, nil
}

var _ maintenance.ContainerLister = dockerLister{}

// backupDeps builds the operator command dependencies from the environment.
func backupDeps(cfg config.Config, log *slog.Logger) control.BackupDeps {
	return control.BackupDeps{
		DataDir:      cfg.DataDir,
		StorePath:    cfg.StorePath,
		ArtifactsDir: filepath.Join(cfg.DataDir, "artifacts"),
		Forbidden:    []string{cfg.ChatsDir, cfg.RepoRoot},
		DefaultRoot:  os.Getenv("GOPOD_BACKUP_DIR"),
		Version:      buildVersion(),
		Log:          log,
		Containers: func(ctx context.Context) (maintenance.ContainerLister, func(), error) {
			d, err := runner.NewDocker(ctx, log)
			if err != nil {
				return nil, nil, err
			}
			return dockerLister{d: d}, func() { _ = d.Close() }, nil
		},
	}
}

// runBackupCLI implements `gopod backup <create|verify|list|idle|quarantine>`.
// It goes through the control Router like every other admin command, never
// opens the application store (no migrations), starts no subsystem and needs
// no sqlite3 binary. The return value is the process exit code.
func runBackupCLI(args []string, out, errw io.Writer) int {
	asJSON, yes := false, false
	var rest []string
	for _, a := range args {
		switch a {
		case "--json":
			asJSON = true
		case "--yes", "-y":
			yes = true
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(errw, "usage: gopod backup <create|verify|list|idle|quarantine> [flags] [--json] [--yes]")
		return 2
	}
	cfg, err := config.Load(true)
	if err != nil {
		fmt.Fprintf(errw, "gopod backup: load config: %v\n", err)
		return 1
	}
	log := slog.New(slog.NewTextHandler(errw, &slog.HandlerOptions{Level: slog.LevelWarn}))
	router := control.New(log)
	control.RegisterBackup(router, backupDeps(cfg, log))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	resp, err := router.Dispatch(ctx, control.Command{
		Name: "backup." + rest[0], Args: rest[1:], Confirmed: yes,
		Caller: control.Caller{Source: control.SourceCLI, IsOwner: true},
	})
	if err != nil {
		fmt.Fprintf(errw, "gopod backup: %v\n", err)
		return 2
	}
	if asJSON {
		b, _ := json.MarshalIndent(map[string]any{"code": resp.Code, "text": resp.Text, "data": resp.Data}, "", "  ")
		fmt.Fprintln(out, string(b))
	} else {
		fmt.Fprintln(out, resp.Text)
	}
	return resp.Code
}
