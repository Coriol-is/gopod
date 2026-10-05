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
//
// The agent-image tests are configurable so the suite can be pointed
// at a freshly built image and pin the harness version it expects:
//
//     GOPOD_TEST_AGENT_IMAGE      image for the Claude tests (default gopod-agent:latest)
//     GOPOD_TEST_CLAUDE_VERSION   if set, `claude --version` output must contain it
//     GOPOD_TEST_CODEX_IMAGE      image for the Codex test (default gopod-agent-codex:latest)
//     GOPOD_TEST_CODEX_VERSION    if set, `codex --version` output must contain it
//
// Example, after bumping the pins in container/Dockerfile*:
//
//     GOPOD_TEST_CLAUDE_VERSION=2.1.289 GOPOD_TEST_CODEX_VERSION=0.160.0 \
//       go test -tags docker_integration -run 'AgentImage|Codex' ./internal/runner/

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

// envOr returns the environment variable key, or def when unset/empty.
func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func agentImage() string { return envOr("GOPOD_TEST_AGENT_IMAGE", "gopod-agent:latest") }
func codexImage() string { return envOr("GOPOD_TEST_CODEX_IMAGE", "gopod-agent-codex:latest") }

// requireImage skips the test when image is not present locally. Agent
// images are built from container/Dockerfile*, never pulled.
func requireImage(t *testing.T, ctx context.Context, d *Docker, image string) {
	t.Helper()
	if _, _, err := d.cli.ImageInspectWithRaw(ctx, image); err != nil {
		t.Skipf("%s not built locally (see container/README.md): %v", image, err)
	}
}

// requireVersion fails the test when wantEnv is set and out does not
// contain its value. With wantEnv unset it only checks the marker.
func requireVersion(t *testing.T, tool, out, marker, wantEnv string) {
	t.Helper()
	if !strings.Contains(out, marker) {
		t.Errorf("%s --version stdout = %q, want to contain %q", tool, out, marker)
	}
	if want := os.Getenv(wantEnv); want != "" && !strings.Contains(out, want) {
		t.Errorf("%s --version stdout = %q, want to contain %s=%q", tool, out, wantEnv, want)
	}
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

	name := "gopod-inttest-ensure"
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

	// This test depends on gopod-agent:latest being built locally.
	// We do NOT pull it (it is not on Docker Hub) — if it is missing,
	// skip rather than fail.
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireImage(t, ctx, d, agentImage())

	name := "gopod-inttest-auth"
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}

	cfg := &container.Config{
		Image:  agentImage(),
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
	// A vanilla gopod-agent:latest with no mounted credentials is
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
// pipeline against gopod-agent:latest. This is the regression
// test that would have caught the seccomp=default and the HOME=/
// bugs that the M6d end-to-end testing surfaced — both lived in
// flag assembly that the older alpine-based tests bypassed.
//
// Skipped if gopod-agent:latest is not built locally.
func TestIntegrationBuildContainerArgsAgainstAgentImage(t *testing.T) {
	d := mkDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireImage(t, ctx, d, agentImage())

	// Tear down any leftover from previous runs.
	name := "gopod-inttest-buildargs"
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
	// already exist on the host inside RepoRoot. Production gopod
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
		Image:       agentImage(),
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
	requireVersion(t, "claude", res.Stdout, "Claude Code", "GOPOD_TEST_CLAUDE_VERSION")
}

// TestIntegrationCodexImageVersion checks the Codex agent image boots
// and reports the pinned CLI version. Skipped when the image is not
// built locally.
func TestIntegrationCodexImageVersion(t *testing.T) {
	d := mkDocker(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireImage(t, ctx, d, codexImage())

	name := "gopod-inttest-codex-version"
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}
	cfg := &container.Config{
		Image:  codexImage(),
		Cmd:    strslice.StrSlice{"sleep", "3600"},
		Labels: map[string]string{LabelChat: "inttest-codex", LabelVersion: "integration-test"},
	}
	id, err := d.EnsureRunning(ctx, name, cfg, &container.HostConfig{})
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })

	res, err := d.Exec(ctx, id, NewCodexProvider("").VersionCmd(), nil)
	if err != nil {
		t.Fatalf("Exec codex --version: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("codex --version exit %d: stdout=%q stderr=%q", res.ExitCode, res.Stdout, res.Stderr)
	}
	requireVersion(t, "codex", res.Stdout, "codex-cli", "GOPOD_TEST_CODEX_VERSION")
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
		if id, _ := d.inspectByName(ctx, "gopod-"+name); id != "" {
			_ = d.Remove(ctx, id)
		}
		created, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, "gopod-"+name)
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
	// gopod-prefixed containers around — we assert AT LEAST 1.
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
