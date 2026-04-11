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
	"errors"
	"io"
	"log/slog"
	"os"
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

func TestIntegrationCheckAuthOnAgentImage(t *testing.T) {
	d := mkDocker(t)

	// This test depends on picoclaw-agent:latest being built locally.
	// We do NOT pull it (it is not on Docker Hub) — if it is missing,
	// skip rather than fail.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, _, err := d.cli.ImageInspectWithRaw(ctx, "picoclaw-agent:latest"); err != nil {
		t.Skipf("picoclaw-agent:latest not built locally (run `docker build -t picoclaw-agent:latest container/`): %v", err)
	}

	name := "picoclaw-inttest-auth"
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}

	cfg := &container.Config{
		Image:  "picoclaw-agent:latest",
		Cmd:    strslice.StrSlice{"sleep", "3600"},
		Labels: map[string]string{LabelChat: "inttest-auth", LabelVersion: "integration-test"},
	}
	host := &container.HostConfig{
		RestartPolicy: container.RestartPolicy{Name: "no"},
	}
	id, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })

	status, err := d.CheckAuth(ctx, id)
	if err != nil {
		t.Fatalf("CheckAuth: %v", err)
	}
	// A vanilla picoclaw-agent:latest with no mounted credentials is
	// definitely not logged in. We assert that exact state because if
	// it ever returns LoggedIn=true on a stock image, our auth gate
	// is broken.
	if status.LoggedIn {
		t.Errorf("LoggedIn = true on stock image (expected false): %+v", status)
	}
	if status.AuthMethod != "none" {
		t.Errorf("AuthMethod = %q, want \"none\"", status.AuthMethod)
	}

	// RunPrompt against the same unauthenticated container must map to
	// ErrNotLoggedIn — that is the contract M6's telegram handler
	// depends on to prompt the user to /login.
	_, err = d.RunPrompt(ctx, id, "hello")
	if !errors.Is(err, ErrNotLoggedIn) {
		t.Errorf("RunPrompt: err = %v, want ErrNotLoggedIn", err)
	}
}

// TestIntegrationBuildContainerArgsAgainstAgentImage exercises the
// full BuildContainerArgs → ContainerCreate → ContainerStart → Exec
// pipeline against picoclaw-agent:latest. This is the regression
// test that would have caught the seccomp=default and the HOME=/
// bugs that the M6d end-to-end testing surfaced — both lived in
// flag assembly that the older alpine-based tests bypassed.
//
// Skipped if picoclaw-agent:latest is not built locally.
func TestIntegrationBuildContainerArgsAgainstAgentImage(t *testing.T) {
	d := mkDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, _, err := d.cli.ImageInspectWithRaw(ctx, "picoclaw-agent:latest"); err != nil {
		t.Skipf("picoclaw-agent:latest not built locally: %v", err)
	}

	// Tear down any leftover from previous runs.
	name := "picoclaw-inttest-buildargs"
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}

	// We need temp host dirs for the bind mounts BuildMounts will
	// emit. Reuse the mounts_test helper layout.
	root := t.TempDir()
	paths := Paths{
		RepoRoot:           root + "/repo",
		DataDir:            root + "/data",
		ChatsDir:           root + "/repo/chats",
		ContainerSkillsDir: root + "/repo/container/skills",
		EmptyFile:          root + "/data/empty-env",
	}
	for _, dir := range []string{paths.RepoRoot, paths.DataDir, paths.ChatsDir, paths.ContainerSkillsDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	// Touch the store.sqlite owner mounts wants to bind RW.
	if err := os.WriteFile(paths.DataDir+"/store.sqlite", []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// The owner-tier .env mask mount overlays EmptyFile onto
	// /workspace/project/.env. Docker cannot create the in-container
	// mountpoint inside a RO bind mount, so the target file must
	// already exist on the host inside RepoRoot. Production picoclaw
	// runs out of a real checkout where .env actually exists; the
	// test fixture has to recreate that.
	if err := os.WriteFile(paths.RepoRoot+"/.env", []byte("# placeholder for .env mask test\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := EnsureChatDirs(paths, "inttest-buildargs", true, nil); err != nil {
		t.Fatalf("EnsureChatDirs: %v", err)
	}

	mounts, err := BuildMounts(paths, "inttest-buildargs", TierOwner, nil)
	if err != nil {
		t.Fatalf("BuildMounts: %v", err)
	}

	cfg, host, _, err := BuildContainerArgs(SpawnConfig{
		Image:       "picoclaw-agent:latest",
		ChatFolder:  "inttest-buildargs",
		Version:     "integration-test",
		Mounts:      mounts,
		UID:         os.Getuid(),
		GID:         os.Getgid(),
		MemoryBytes: 4 << 30,
		NanoCPUs:    2_000_000_000,
		PidsLimit:   1024,
	})
	if err != nil {
		t.Fatalf("BuildContainerArgs: %v", err)
	}

	id, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		// This is the line that was failing with the seccomp bug:
		// `Error response from daemon: Decoding seccomp profile failed`
		// Any future regression in flag assembly that prevents
		// ContainerStart from succeeding will surface here.
		t.Fatalf("EnsureRunning via BuildContainerArgs: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })

	// HOME must be /home/node inside the container after the M6
	// HOME-fix. Verifying via Exec is the matching half of the
	// docker_args_test.go unit assertion.
	res, err := d.Exec(ctx, id, []string{"sh", "-c", "echo $HOME"}, nil)
	if err != nil {
		t.Fatalf("Exec echo HOME: %v", err)
	}
	if got := strings.TrimSpace(res.Stdout); got != "/home/node" {
		t.Errorf("HOME inside container = %q, want /home/node", got)
	}

	// $HOME must be writable. Without the /home/node tmpfs the
	// rootfs is RO and this would fail with EROFS.
	res, err = d.Exec(ctx, id, []string{"sh", "-c", "touch /home/node/test && echo ok"}, nil)
	if err != nil {
		t.Fatalf("Exec touch HOME: %v", err)
	}
	if res.ExitCode != 0 || strings.TrimSpace(res.Stdout) != "ok" {
		t.Errorf("touch /home/node failed: exit=%d stdout=%q stderr=%q",
			res.ExitCode, res.Stdout, res.Stderr)
	}

	// claude --version must produce its identification line on
	// stdout. The bug we are guarding against (claude exits 0 with
	// empty stdout) was specifically about silent EROFS failures
	// during state writes; --version is the simplest claude
	// invocation that still touches enough of its init path to
	// catch them.
	res, err = d.Exec(ctx, id, []string{"claude", "--version"}, nil)
	if err != nil {
		t.Fatalf("Exec claude --version: %v", err)
	}
	if !strings.Contains(res.Stdout, "Claude Code") {
		t.Errorf("claude --version stdout = %q, want to contain 'Claude Code'", res.Stdout)
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
