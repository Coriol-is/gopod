package runner

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// InteractiveExec represents a running `docker exec` session with
// both stdin (writable) and stdout (readable) attached, plus a TTY
// allocated so Ink-based prompts (like `claude auth login`) render
// and accept input correctly.
//
// With Tty=true, Docker merges stdout and stderr into a single stream
// (no stdcopy demux header). Reading lines from Stdout gives the
// combined terminal output exactly as the user would see it in
// `docker exec -it`.
//
// Lifecycle:
//
//  1. Created by (*Docker).ExecInteractive.
//  2. Caller reads lines via ReadLine (blocks until a line is available).
//  3. Caller writes to stdin via WriteStdin (e.g. the OAuth code).
//  4. Caller closes via Close when done. Closing the conn signals EOF
//     to the process's stdin; the process may then exit naturally.
//  5. Caller can check exit code via ExitCode (polls ExecInspect).
type InteractiveExec struct {
	reader *bufio.Reader
	conn   net.Conn
	execID string
	d      *Docker
}

// ExecInteractive starts an interactive exec session inside the given
// container with a TTY and stdin attached. The returned
// InteractiveExec must be Close()d by the caller.
//
// This is the primitive M6.5's /login handler uses to run
// `claude auth login` and feed the OAuth code back into claude's
// stdin after the user pastes it via Telegram.
func (d *Docker) ExecInteractive(
	ctx context.Context,
	containerID string,
	cmd []string,
	env []string,
) (*InteractiveExec, error) {
	if len(cmd) == 0 {
		return nil, fmt.Errorf("docker: ExecInteractive: empty cmd")
	}

	created, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		Tty:          true, // allocate PTY so Ink prompts work
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec interactive create: %w", err)
	}

	resp, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{
		Tty: true,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec interactive attach: %w", err)
	}

	return &InteractiveExec{
		reader: resp.Reader,
		conn:   resp.Conn,
		execID: created.ID,
		d:      d,
	}, nil
}

// ReadLine reads one line from the combined stdout+stderr stream.
// Blocks until a full line (terminated by '\n') is available or the
// stream closes. Returns io.EOF when the exec process exits.
//
// TTY output may include ANSI escape codes and carriage returns;
// callers that need plain text should strip them.
func (ie *InteractiveExec) ReadLine() (string, error) {
	line, err := ie.reader.ReadString('\n')
	// Trim trailing \r\n — TTY output uses \r\n line endings.
	line = strings.TrimRight(line, "\r\n")
	return line, err
}

// WriteStdin sends data to the exec process's stdin. The caller is
// responsible for appending a newline if the process expects one
// (e.g. `code + "\n"`).
func (ie *InteractiveExec) WriteStdin(data []byte) (int, error) {
	return ie.conn.Write(data)
}

// Close tears down the interactive session. The underlying net.Conn
// is closed, which signals EOF on the process's stdin. If the
// process is still running it may continue or exit depending on how
// it handles stdin closure.
func (ie *InteractiveExec) Close() error {
	if ie.conn != nil {
		return ie.conn.Close()
	}
	return nil
}

// ExitCode polls ContainerExecInspect and returns the exit code once
// the process has finished. If still running, returns (-1, nil).
func (ie *InteractiveExec) ExitCode(ctx context.Context) (int, error) {
	info, err := ie.d.cli.ContainerExecInspect(ctx, ie.execID)
	if err != nil {
		return -1, fmt.Errorf("docker: exec inspect: %w", err)
	}
	if info.Running {
		return -1, nil
	}
	return info.ExitCode, nil
}

// DrainAndClose reads all remaining output until EOF and then closes.
// Useful for cleanup when the caller no longer cares about the output
// but wants to ensure the connection resources are released.
func (ie *InteractiveExec) DrainAndClose() {
	if ie.conn != nil {
		_, _ = io.Copy(io.Discard, ie.reader)
		_ = ie.conn.Close()
	}
}
