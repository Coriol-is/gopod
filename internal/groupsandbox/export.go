package groupsandbox

import (
	"bytes"
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// Caller proves only the differently-owned trusted supervisor remains. Native
// credentials and host files are never present; tar reads only this tmpfs.
func (e *Executor) export(ctx context.Context, id, scope string, validate func(context.Context) error) error {
	x, err := e.Client.ContainerExecCreate(ctx, id, container.ExecOptions{User: e.user(), WorkingDir: "/", Cmd: []string{"tar", "-cf", "-", "-C", "/workspace/chat", "."}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return ErrUnavailable
	}
	a, err := e.Client.ContainerExecAttach(ctx, x.ID, container.ExecAttachOptions{})
	if err != nil {
		return ErrUnavailable
	}
	defer a.Close()
	stop := context.AfterFunc(ctx, func() { a.Close() })
	defer stop()
	r, w := io.Pipe()
	done := make(chan error, 1)
	go func() { _, err := stdcopy.StdCopy(w, io.Discard, a.Reader); _ = w.CloseWithError(err); done <- err }()
	prepared, saveErr := archiveMode(r, e.UID, e.GID, true)
	if saveErr == nil {
		if n, err := io.Copy(io.Discard, io.LimitReader(r, 65537)); err != nil || n > 65536 {
			saveErr = ErrUnavailable
		}
	}
	_ = r.Close()
	if saveErr != nil {
		a.Close()
	}
	readErr := <-done
	state, err := e.waitExec(ctx, x.ID)
	if err != nil || state.Running || state.ExitCode != 0 || saveErr != nil || readErr != nil {
		return ErrUnavailable
	}
	return e.saveChecked(ctx, scope, bytes.NewReader(prepared), validate)
}
