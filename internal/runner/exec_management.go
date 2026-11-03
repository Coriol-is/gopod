package runner

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// probeCodexManagement uses a non-TTY duplex attachment. Docker context
// cancellation alone does not stop exec, so every exit kills this exact owned
// process group, verifies the exec stopped, then frees its caller's chat slot.
func (r *Runner) probeCodexManagement(ctx context.Context, id string) (report ManagementReport, retErr error) {
	ctx, finishOperation := r.operationContext(ctx)
	defer finishOperation()
	if err := ctx.Err(); err != nil {
		return report, err
	}
	pidFile, err := newAgentPidFile()
	if err != nil {
		return report, err
	}
	created, err := r.d.cli.ContainerExecCreate(ctx, id, container.ExecOptions{
		Cmd:         wrapWithPidFile(pidFile, []string{"codex", "app-server", "--listen", "stdio://"}),
		AttachStdin: true, AttachStdout: true, AttachStderr: true, Tty: false,
	})
	if err != nil {
		return report, err
	}
	// An attach failure may be ambiguous: cleanup is needed even when no
	// attachment was returned to us. Never emit Docker/raw CLI errors to logs.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), managementCleanupReserve)
		defer cancel()
		res, err := r.ex.Exec(cleanup, id, killAgentCmd(pidFile, 100*time.Millisecond), nil)
		if err != nil || res == nil || res.ExitCode != 0 {
			r.markContainerUnknown(ctx, id)
			report.ErrorClass = "cleanup_failed"
			retErr = errors.New("management cleanup failed")
			return
		}
		state, err := r.d.cli.ContainerExecInspect(cleanup, created.ID)
		if err != nil || state.Running {
			r.markContainerUnknown(ctx, id)
			report.ErrorClass = "cleanup_failed"
			retErr = errors.New("management cleanup unverified")
		}
	}()
	attached, err := r.d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{Tty: false})
	if err != nil {
		return report, err
	}
	stdout, writer := io.Pipe()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := stdcopy.StdCopy(writer, io.Discard, attached.Reader)
		_ = writer.CloseWithError(err)
	}()
	stop := context.AfterFunc(ctx, func() { attached.Close(); _ = stdout.CloseWithError(ctx.Err()) })
	defer func() { stop(); attached.Close(); _ = stdout.Close(); _ = writer.Close(); <-done }()
	return codexManagementProtocol(ctx, attached.Conn, stdout)
}
