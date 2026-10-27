package groupsandbox

import (
	"bytes"
	"context"
	"io"

	"github.com/docker/docker/api/types/container"
)

// Docker's archive API cannot restore a tmpfs under a readonly rootfs on all
// engines. Extract a prevalidated canonical archive inside the credential-free
// runtime instead; it still has no host mounts, network or privilege.
func (e *Executor) hydrate(ctx context.Context, id string, data []byte) error {
	x, err := e.Client.ContainerExecCreate(ctx, id, container.ExecOptions{User: e.user(), WorkingDir: "/", Cmd: []string{"tar", "--no-same-owner", "-xf", "-", "-C", "/workspace/chat"}, AttachStdin: true, AttachStdout: true, AttachStderr: true})
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
	wrote := make(chan error, 1)
	go func() {
		_, err := io.Copy(a.Conn, bytes.NewReader(data))
		if err == nil {
			err = a.CloseWrite()
		}
		wrote <- err
	}()
	_, readErr := io.Copy(io.Discard, a.Reader)
	writeErr := <-wrote
	state, err := e.waitExec(ctx, x.ID)
	if err != nil || readErr != nil || writeErr != nil || state.Running || state.ExitCode != 0 || ctx.Err() != nil {
		return ErrUnavailable
	}
	return nil
}
