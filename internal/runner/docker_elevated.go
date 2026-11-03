package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// ExecElevated cancels via the supervisor's private stdin, never a host PID.
// A cancelled attachment is not exit proof: inspect continues on a bounded context.
func (d *Docker) ExecElevated(ctx context.Context, id string, argv []string) (*ExecResult, error) {
	cmd := append([]string{"node", "/usr/local/lib/gopod-elevated-exec.cjs"}, argv...)
	created, err := d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{Cmd: cmd, AttachStdin: true, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return nil, err
	}
	// Once create succeeds, cancellation cannot hide whether the exec was attached.
	attachCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	resp, err := d.cli.ContainerExecAttach(attachCtx, created.ID, container.ExecAttachOptions{})
	cancel()
	if err != nil {
		return nil, fmt.Errorf("%w: elevated attach: %v", ErrExecutionUnproven, err)
	}
	defer resp.Close()
	out, stderr := &boundedBuffer{}, &boundedBuffer{}
	copied := make(chan error, 1)
	go func() { _, e := stdcopy.StdCopy(out, stderr, resp.Reader); copied <- e }()
	stop := context.AfterFunc(ctx, func() { _ = resp.CloseWrite() })
	defer stop()
	// Wait for attachment EOF before treating Running=false as exit. Immediately
	// after attach the daemon may still report a created-but-not-started exec.
	var copyErr error
	select {
	case copyErr = <-copied:
	case <-ctx.Done():
		_ = resp.CloseWrite()
		select {
		case copyErr = <-copied:
		case <-time.After(15 * time.Second):
			resp.Close()
			<-copied
			return nil, ErrExecutionUnproven
		}
	}
	inspectCtx, finish := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finish()
	for {
		info, e := d.cli.ContainerExecInspect(inspectCtx, created.ID)
		if e != nil {
			return nil, fmt.Errorf("%w: elevated exit inspection: %v", ErrExecutionUnproven, e)
		}
		if !info.Running {
			return &ExecResult{Stdout: out.String(), Stderr: stderr.String(), ExitCode: info.ExitCode}, copyErr
		}
		select {
		case <-inspectCtx.Done():
			return nil, ErrExecutionUnproven
		case <-time.After(50 * time.Millisecond):
		}
	}

}

// ElevatedIDs identifies renamed survivors by immutable Docker labels.
func (d *Docker) ElevatedIDs(ctx context.Context) ([]string, error) {
	rows, err := d.ListGopodContainers(ctx)
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, r := range rows {
		if r.Labels[LabelPolicyRole] == string(RoleElevatedHost) {
			ids = append(ids, r.ID)
		}
	}
	return ids, nil
}

// ElevatedSupported refuses to promise unenforced resource bounds.
func (d *Docker) ElevatedSupported(ctx context.Context) error {
	info, err := d.cli.Info(ctx)
	if err != nil {
		return err
	}
	if !info.MemoryLimit || !info.PidsLimit || !info.CPUCfsQuota || info.NCPU < 1 {
		return fmt.Errorf("elevated requires Docker memory and pids cgroup enforcement; host qualification is incomplete")
	}
	return nil
}
