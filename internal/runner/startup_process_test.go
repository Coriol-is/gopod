package runner

import (
	"context"
	"errors"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// localProbeProcessExec models Docker's detached exec cancellation: returning
// from Exec closes attachment without killing the already-started subprocess.
type localProbeProcessExec struct {
	*fakeAgentExec
	started chan int
	exited  chan struct{}
}

func (e *localProbeProcessExec) Exec(ctx context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	process := exec.Command(cmd[0], cmd[1:]...)
	process.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := process.Start(); err != nil {
		return nil, err
	}
	done := make(chan error, 1)
	wrapped := len(cmd) > 4 && cmd[0] == "sh"
	go func() {
		err := process.Wait()
		done <- err
		if wrapped {
			close(e.exited)
		}
	}()
	if wrapped {
		e.started <- process.Process.Pid
	}
	select {
	case err := <-done:
		if err != nil {
			return &ExecResult{ExitCode: process.ProcessState.ExitCode()}, nil
		}
		return &ExecResult{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func TestLocalProbeCancellationStopsRealProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no shell")
	}
	e := &localProbeProcessExec{fakeAgentExec: newFakeAgentExec(), started: make(chan int, 1), exited: make(chan struct{})}
	r := &Runner{ex: e, log: quietLog()}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		_, err := r.execLocalProbe(ctx, "local", []string{"sh", "-c", "sleep 300 & wait"})
		done <- err
	}()
	pid := <-e.started
	// If the test fails before cleanup, do not leave the test process alive.
	defer syscall.Kill(-pid, syscall.SIGKILL)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(6 * time.Second):
		t.Fatal("probe cleanup exceeded reserve")
	}
	select {
	case <-e.exited:
	case <-time.After(time.Second):
		t.Fatal("probe main process survived cancellation")
	}
	if err := syscall.Kill(-pid, 0); err == nil {
		t.Fatal("probe process group survived cleanup")
	}
}
