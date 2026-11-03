//go:build integration

package runner

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
)

func TestElevatedExecSupervisorDocker(t *testing.T) {
	image := os.Getenv("GOPOD_ELEVATED_TEST_IMAGE")
	if image == "" {
		t.Skip("set GOPOD_ELEVATED_TEST_IMAGE to a built elevated image")
	}
	d, err := NewDocker(t.Context(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.CreateStart(t.Context(), "gopod-elevated-supervisor-test", &container.Config{Image: image, Cmd: []string{"sleep", "infinity"}}, &container.HostConfig{NetworkMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(ctx, id)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := d.ExecElevated(ctx, id, []string{"sh", "-c", "printf supervisor-ok; exit 7"})
	if err != nil || r.ExitCode != 7 || r.Stdout != "supervisor-ok" {
		t.Fatal(r, err)
	}
	stopCtx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan *ExecResult, 1)
	errCh := make(chan error, 1)
	go func() { r, e := d.ExecElevated(stopCtx, id, []string{"sh", "-c", "sleep 60"}); done <- r; errCh <- e }()
	// Wait for a real child, rather than cancelling before exec attachment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		top, e := d.cli.ContainerTop(ctx, id, []string{"-eo", "pid,args"})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, row := range top.Processes {
			if strings.Contains(strings.Join(row, " "), "sleep 60") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child not started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	select {
	case r = <-done:
	case <-ctx.Done():
		t.Fatal("cancellation did not settle")
	}
	err = <-errCh
	if err != nil || r == nil || r.ExitCode != 137 {
		t.Fatal(r, err)
	}
	if idle, e := d.quiescent(ctx, id); e != nil || !idle {
		t.Fatal("child survived", e)
	}
	r, err = d.ExecElevated(ctx, id, []string{"echo", "container-still-live"})
	if err != nil || !strings.Contains(r.Stdout, "container-still-live") {
		t.Fatal(r, err)
	}
}
