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
	"strings"
	"syscall"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/runner"
)

// composeServiceLabel and composeOneOffLabel are set by Docker Compose. The
// controller itself is a container of the "gopod" service; a one-off
// `docker compose run` capture container carries the oneoff label and is not
// a writer.
const (
	composeServiceLabel = "com.docker.compose.service"
	composeOneOffLabel  = "com.docker.compose.oneoff"
	composeServiceName  = "gopod"
)

func isLive(state string) bool {
	return state == "running" || state == "restarting" || state == "paused"
}

// liveOwned picks the live agent containers and the live (non one-off)
// controller containers of the compose service. A controller left over from a
// release that predates the instance lock does not hold it, so the lock alone
// cannot prove it is gone; the container inventory can (best effort).
func liveOwned(agents, services []container.Summary) []string {
	var ids []string
	for _, c := range agents {
		if isLive(c.State) {
			ids = append(ids, c.ID)
		}
	}
	for _, c := range services {
		if isLive(c.State) && !strings.EqualFold(c.Labels[composeOneOffLabel], "true") {
			ids = append(ids, c.ID)
		}
	}
	return ids
}

// dockerLister reports live owned containers through the Docker SDK. It can
// only list: the backup fence has no way to stop or remove a container.
type dockerLister struct{ d *runner.Docker }

func (l dockerLister) LiveOwned(ctx context.Context) ([]string, error) {
	agents, err := l.d.ListGopodContainers(ctx)
	if err != nil {
		return nil, err
	}
	services, err := l.d.Client().ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.KeyValuePair{Key: "label", Value: composeServiceLabel + "=" + composeServiceName},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("docker: list compose service containers: %w", err)
	}
	return liveOwned(agents, services), nil
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
		ScratchDir:   os.Getenv("GOPOD_BACKUP_SCRATCH_DIR"),
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
