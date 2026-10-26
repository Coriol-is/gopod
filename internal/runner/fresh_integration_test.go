//go:build docker_integration

package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/docker/docker/api/types/container"
)

type sleepingSystemProvider struct{ *ClaudeProvider }

func (p sleepingSystemProvider) RunFreshCmd(string) []string {
	return []string{"sh", "-c", "echo $$ > /tmp/system-process; sleep 120 & wait"}
}

// Reproduce a silent background child, cancel it for a foreground waiter, and
// verify the actual process is gone before the per-chat slot can be reused.
// No provider login, network or model call is used.
func TestIntegrationFreshPreemptionKillsProcess(t *testing.T) {
	d := mkDocker(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	img := agentImage()
	requireImage(t, ctx, d, img)
	name := fmt.Sprintf("gopod-inttest-fresh-%d", time.Now().UnixNano())
	cfg := &container.Config{Image: img, User: "1000:1000", Cmd: []string{"sleep", "infinity"}}
	host := &container.HostConfig{ReadonlyRootfs: true, NetworkMode: "none", CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges"}, Tmpfs: map[string]string{"/tmp": "rw,noexec,nosuid,size=16m,mode=1777"}}
	id, err := d.EnsureRunning(ctx, name, cfg, host)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.Remove(context.Background(), id) })
	r := &Runner{profilePolicy: testPersonalPolicy(), ex: d, provider: sleepingSystemProvider{NewClaudeProvider(img)}, log: testLogger()}
	r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) { return id, nil }
	done := make(chan error, 1)
	go func() { _, err := r.RunFresh(ctx, "chat", TierRegistered, nil, "ignored"); done <- err }()
	for {
		res, err := d.Exec(ctx, id, []string{"test", "-s", "/tmp/system-process"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if res.ExitCode == 0 {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("background did not start")
		case <-time.After(20 * time.Millisecond):
		}
	}
	release, err := r.acquireAgentSlot(ctx, "chat")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("fresh result=%v", err)
	}
	res, err := d.Exec(ctx, id, []string{"sh", "-c", `p=$(cat /tmp/system-process); if kill -0 "$p" 2>/dev/null; then echo alive; exit 1; fi; echo stopped`}, nil)
	if err != nil || res.ExitCode != 0 || !strings.Contains(res.Stdout, "stopped") {
		t.Fatalf("process cleanup: %+v %v", res, err)
	}
}
