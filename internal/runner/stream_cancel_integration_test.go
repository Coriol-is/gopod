//go:build docker_integration

package runner

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/strslice"
)

// shImage is any small local image with a POSIX sh. Not pulled: set
// GOPOD_TEST_SH_IMAGE to an image you already have (e.g. redis:7-alpine).
func shImage() string { return envOr("GOPOD_TEST_SH_IMAGE", integrationImage) }

// noRestoreProvider is the claude provider minus the config restore
// exec, which has nothing to restore in a plain image.
type noRestoreProvider struct{ *ClaudeProvider }

func (noRestoreProvider) RestoreConfigCmd() []string { return nil }

// startShContainer starts a throwaway container shaped like an agent
// container where it matters here: non-root user, read-only rootfs,
// /tmp as tmpfs. Removed on cleanup.
func startShContainer(t *testing.T, d *Docker, name string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	requireImage(t, ctx, d, shImage())
	if id, _ := d.inspectByName(ctx, name); id != "" {
		_ = d.Remove(ctx, id)
	}
	cfg := &container.Config{
		Image:      shImage(),
		Entrypoint: strslice.StrSlice{"sleep"},
		Cmd:        strslice.StrSlice{"120"},
		User:       "1000:1000",
		Labels:     map[string]string{LabelChat: name, LabelVersion: "integration-test"},
	}
	host := &container.HostConfig{
		RestartPolicy:  container.RestartPolicy{Name: "no"},
		ReadonlyRootfs: true,
		Tmpfs:          map[string]string{"/tmp": "rw,size=16m,mode=1777"},
	}
	id, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		t.Fatalf("EnsureRunning: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Remove(ctx, id)
	})
	return id
}

func integrationStreamRunner(d *Docker) *Runner {
	return &Runner{
		d:            d,
		ex:           d,
		log:          testLogger(),
		turnCount:    make(map[convKey]int),
		activity:     make(map[string]time.Time),
		compactAfter: 30,
	}
}

// containerState lists the command lines of live processes and the
// gopod pid files in the container.
func containerState(t *testing.T, d *Docker, id string) (procs, pidFiles string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	res, err := d.Exec(ctx, id, []string{"sh", "-c",
		`for p in /proc/[0-9]*; do c=$(tr '\0' ' ' < $p/cmdline 2>/dev/null); [ -n "$c" ] && echo "$c"; done; echo ---; ls /tmp`}, nil)
	if err != nil {
		t.Fatalf("inspect container: %v", err)
	}
	procs, pidFiles, _ = strings.Cut(res.Stdout, "---")
	return procs, pidFiles
}

func TestIntegrationStreamCancelKillsAgent(t *testing.T) {
	d := mkDocker(t)
	id := startShContainer(t, d, "gopod-inttest-streamcancel")
	r := integrationStreamRunner(d)
	prov := noRestoreProvider{NewClaudeProvider("unused")}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// An "agent" that prints, then runs a long tool in a child process.
	cmd := []string{"sh", "-c", "echo started; sleep 300; echo unreachable"}
	stream, err := r.startStream(ctx, id, convKey{"inttest", 0}, TierRegistered, nil, prov, "p", cmd)
	if err != nil {
		t.Fatalf("startStream: %v", err)
	}
	select {
	case c := <-stream.Chunks:
		if !strings.Contains(c, "started") {
			t.Fatalf("first chunk = %q", c)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("agent never started")
	}
	if procs, _ := containerState(t, d, id); !strings.Contains(procs, "sleep 300") {
		t.Fatalf("agent not running before cancel:\n%s", procs)
	}

	cancelled := time.Now()
	cancel()
	go func() {
		for range stream.Chunks {
		}
	}()
	select {
	case res := <-stream.Result:
		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("result err = %v, want context.Canceled", res.Err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("no result within 6s of cancel")
	}
	t.Logf("result %v after cancel", time.Since(cancelled))

	procs, files := containerState(t, d, id)
	if strings.Contains(procs, "sleep 300") || strings.Contains(procs, "echo started") {
		t.Errorf("agent still running %v after cancel:\n%s", time.Since(cancelled), procs)
	}
	if strings.Contains(files, "gopod-agent-") {
		t.Errorf("pid file left behind: %s", files)
	}
}

func TestIntegrationStreamNormalRunRemovesPidFile(t *testing.T) {
	d := mkDocker(t)
	id := startShContainer(t, d, "gopod-inttest-streamok")
	r := integrationStreamRunner(d)
	prov := noRestoreProvider{NewClaudeProvider("unused")}

	stream, err := r.startStream(context.Background(), id, convKey{"inttest", 0}, TierRegistered, nil, prov, "p",
		[]string{"sh", "-c", `printf '%s\n' "$1"`, "sh", `it's "quoted"`})
	if err != nil {
		t.Fatalf("startStream: %v", err)
	}
	for range stream.Chunks {
	}
	res := <-stream.Result
	if res.Err != nil || res.FullText != `it's "quoted"` {
		t.Fatalf("result = %+v", res)
	}
	if _, files := containerState(t, d, id); strings.Contains(files, "gopod-agent-") {
		t.Errorf("pid file left behind: %s", files)
	}
}
