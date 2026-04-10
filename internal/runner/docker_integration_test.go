//go:build docker_integration

// This file is excluded from the default `go test ./...` run. To run
// it against a live Docker daemon:
//
//     go test -tags docker_integration ./internal/runner/...
//
// The integration suite exercises EnsureRunning, Exec, Stop, Remove,
// and CleanupLeftovers against the `alpine:latest` image. It expects
// Docker Desktop (or any daemon) to be reachable via the standard
// environment (DOCKER_HOST, or the default socket). The image is
// pulled on demand if absent, which takes a few seconds the first
// time.

package runner

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/strslice"
)

const integrationImage = "alpine:latest"

// testLogger discards output so integration logs don't clutter test runs.
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func ensureAlpinePulled(t *testing.T, d *Docker) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	rc, err := d.cli.ImagePull(ctx, integrationImage, image.PullOptions{})
	if err != nil {
		t.Fatalf("image pull: %v", err)
	}
	defer rc.Close()
	_, _ = io.Copy(io.Discard, rc)
}

func mkDocker(t *testing.T) *Docker {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	d, err := NewDocker(ctx, testLogger())
	if err != nil {
		t.Skipf("docker daemon unavailable: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d
}

func alpineSpec(name string) (*container.Config, *container.HostConfig) {
	cfg := &container.Config{
		Image: integrationImage,
		Cmd:   strslice.StrSlice{"sleep", "3600"},
		User:  "1000:1000",
		Labels: map[string]string{
			LabelChat:    name,
			LabelVersion: "integration-test",
		},
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}
	return cfg, host
}

func TestIntegrationEnsureRunningExecStopRemove(t *testing.T) {
	d := mkDocker(t)
	ensureAlpinePulled(t, d)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	name := "picoclaw-inttest-ensure"
	// Pre-clean in case a previous failed run left it around.
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}

	cfg, host := alpineSpec("inttest-ensure")
	id, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })

	// Idempotent: second call returns the same id without a new container.
	id2, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		t.Fatalf("EnsureRunning idempotent: %v", err)
	}
	if id2 != id {
		t.Errorf("EnsureRunning returned different id on second call: %s vs %s", id, id2)
	}

	// Exec a simple command and verify stdout.
	res, err := d.Exec(ctx, id, []string{"echo", "hello from alpine"}, nil)
	if err != nil {
		t.Fatalf("Exec: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("exit code = %d, want 0 (stderr: %q)", res.ExitCode, res.Stderr)
	}
	if !strings.Contains(res.Stdout, "hello from alpine") {
		t.Errorf("stdout = %q, want to contain hello from alpine", res.Stdout)
	}

	// Exec a failing command.
	res, err = d.Exec(ctx, id, []string{"sh", "-c", "exit 7"}, nil)
	if err != nil {
		t.Fatalf("Exec (fail): %v", err)
	}
	if res.ExitCode != 7 {
		t.Errorf("exit code = %d, want 7", res.ExitCode)
	}

	// Stop + remove.
	if err := d.Stop(ctx, id, 2*time.Second); err != nil {
		t.Errorf("Stop: %v", err)
	}
	if err := d.Remove(ctx, id); err != nil {
		t.Errorf("Remove: %v", err)
	}
}

func TestIntegrationCleanupLeftovers(t *testing.T) {
	d := mkDocker(t)
	ensureAlpinePulled(t, d)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Spawn two containers: one with the "current" version, one stale.
	currentVersion := "current-v1"

	spawn := func(name, version string) string {
		cfg, host := alpineSpec(name)
		cfg.Labels[LabelVersion] = version
		// Pre-clean by name.
		if id, _ := d.inspectByName(ctx, "picoclaw-"+name); id != "" {
			_ = d.Remove(ctx, id)
		}
		created, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, "picoclaw-"+name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if err := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
			t.Fatalf("start %s: %v", name, err)
		}
		return created.ID
	}

	keepID := spawn("inttest-keep", currentVersion)
	staleID := spawn("inttest-stale", "old-v0")

	t.Cleanup(func() {
		_ = d.Remove(context.Background(), keepID)
		_ = d.Remove(context.Background(), staleID)
	})

	cleaned, err := d.CleanupLeftovers(ctx, currentVersion)
	if err != nil {
		t.Fatalf("CleanupLeftovers: %v", err)
	}
	// Stale container should be cleaned; keep container should not. But
	// depending on leftover state from other tests there may be more
	// picoclaw-prefixed containers around — we assert AT LEAST 1.
	if cleaned < 1 {
		t.Errorf("cleaned = %d, want >= 1", cleaned)
	}

	// keepID must still exist.
	if _, err := d.cli.ContainerInspect(ctx, keepID); err != nil {
		t.Errorf("keep container no longer exists after cleanup: %v", err)
	}

	// staleID must be gone.
	if _, err := d.cli.ContainerInspect(ctx, staleID); err == nil {
		t.Error("stale container still exists after cleanup")
	}
}
