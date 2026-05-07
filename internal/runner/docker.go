package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

// LabelChat and LabelVersion are the Docker labels gopod puts on
// every container it spawns. CleanupLeftovers uses them to identify
// leftover containers from prior gopod runs.
const (
	LabelChat    = "gopod.chat"
	LabelVersion = "gopod.version"
)

// Docker is gopod's thin wrapper around the Docker SDK client. It
// centralises ctx-aware versions of the handful of operations the
// runner actually needs (create/start/inspect/exec/stop/remove/list)
// and hides the two or three Docker type aliases we don't want
// leaking into lifecycle.go.
//
// One Docker value per gopod process. Safe for concurrent use —
// the underlying *client.Client is.
type Docker struct {
	cli *client.Client
	log *slog.Logger
}

// NewDocker opens a Docker client using DOCKER_HOST / DOCKER_TLS_*
// from the environment, then negotiates the API version with the
// daemon so this build keeps working across Docker Desktop upgrades.
func NewDocker(ctx context.Context, log *slog.Logger) (*Docker, error) {
	if log == nil {
		log = slog.Default()
	}
	cli, err := client.NewClientWithOpts(
		client.FromEnv,
		client.WithAPIVersionNegotiation(),
	)
	if err != nil {
		return nil, fmt.Errorf("docker: new client: %w", err)
	}

	// Ping to fail fast if the daemon is not reachable. Without this
	// the first real operation would fail with a confusing error.
	if _, err := cli.Ping(ctx); err != nil {
		_ = cli.Close()
		return nil, fmt.Errorf("docker: ping daemon: %w", err)
	}
	log.Info("docker client ready", slog.String("api_version", cli.ClientVersion()))
	return &Docker{cli: cli, log: log}, nil
}

// Close releases the underlying HTTP client. Safe to call multiple times.
func (d *Docker) Close() error {
	if d == nil || d.cli == nil {
		return nil
	}
	return d.cli.Close()
}

// Client returns the underlying Docker SDK client. Escape hatch for
// tests and for M6+ code that needs operations this wrapper doesn't
// yet expose.
func (d *Docker) Client() *client.Client { return d.cli }

// EnsureRunning guarantees that a container named `name` is running
// with the given Config/HostConfig. Behaviour matrix:
//
//   - no container with that name → create + start, return new id.
//   - container exists and is running → return existing id.
//   - container exists but is stopped/exited → remove it (so the
//     fresh spawn picks up any new mount/flag changes), then create
//     + start.
//
// The (cfg, host) pair is applied only when a new container is
// created. gopod does not mutate an already-running container's
// config — stop/remove/recreate is the only supported update path.
func (d *Docker) EnsureRunning(
	ctx context.Context,
	name string,
	cfg *container.Config,
	host *container.HostConfig,
) (string, error) {
	existing, err := d.inspectByName(ctx, name)
	if err != nil {
		return "", err
	}
	if existing != "" {
		// Decide based on current state.
		info, err := d.cli.ContainerInspect(ctx, existing)
		if err != nil {
			return "", fmt.Errorf("docker: inspect %q: %w", name, err)
		}
		if info.State != nil && info.State.Running {
			return existing, nil
		}
		// Stopped leftover. Remove before re-creating.
		d.log.Info("removing stopped leftover before respawn",
			slog.String("name", name),
			slog.String("id", existing[:12]))
		if err := d.cli.ContainerRemove(ctx, existing, container.RemoveOptions{
			Force: true,
		}); err != nil {
			return "", fmt.Errorf("docker: remove stopped %q: %w", name, err)
		}
	}

	created, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("docker: create %q: %w", name, err)
	}
	if err := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		// Best-effort cleanup of the half-created container so we do not
		// leave garbage for the next EnsureRunning call to trip over.
		_ = d.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("docker: start %q: %w", name, err)
	}
	d.log.Info("container spawned",
		slog.String("name", name),
		slog.String("id", created.ID[:12]))
	return created.ID, nil
}

// inspectByName returns the container id of the container whose name
// exactly matches `name`, or empty string if there is none. Docker's
// name filter is a prefix-ish regex in practice, so we re-check.
func (d *Docker) inspectByName(ctx context.Context, name string) (string, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.KeyValuePair{Key: "name", Value: "^/" + name + "$"},
		),
	})
	if err != nil {
		return "", fmt.Errorf("docker: list by name %q: %w", name, err)
	}
	for _, c := range list {
		for _, n := range c.Names {
			if n == "/"+name {
				return c.ID, nil
			}
		}
	}
	return "", nil
}

// ExecResult is the return of Exec: captured stdout, stderr, and the
// exit code of the executed command inside the container.
type ExecResult struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Exec runs `cmd` inside the container via ContainerExecCreate +
// ContainerExecAttach. Stdout and stderr are demultiplexed via
// stdcopy.StdCopy and returned as strings. Env is a key=value slice
// applied on top of the container's own environment.
//
// This is the synchronous variant used by M6.5 for the interactive
// login flow; M6's agent loop will eventually want a streaming
// variant that writes to io.Writer sinks as output arrives. Adding
// that later is additive.
func (d *Docker) Exec(
	ctx context.Context,
	containerID string,
	cmd []string,
	env []string,
) (*ExecResult, error) {
	if len(cmd) == 0 {
		return nil, errors.New("docker: Exec: empty cmd")
	}
	created, err := d.cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return nil, fmt.Errorf("docker: exec create: %w", err)
	}
	resp, err := d.cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return nil, fmt.Errorf("docker: exec attach: %w", err)
	}
	defer resp.Close()

	var outBuf, errBuf bytes.Buffer
	if _, err := stdcopy.StdCopy(&outBuf, &errBuf, resp.Reader); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("docker: exec demux: %w", err)
	}

	// Poll ExecInspect until Running is false. Docker does not give us
	// a channel for this; short poll is the idiomatic shape.
	var exitCode int
	for {
		info, err := d.cli.ContainerExecInspect(ctx, created.ID)
		if err != nil {
			return nil, fmt.Errorf("docker: exec inspect: %w", err)
		}
		if !info.Running {
			exitCode = info.ExitCode
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}

	return &ExecResult{
		Stdout:   outBuf.String(),
		Stderr:   errBuf.String(),
		ExitCode: exitCode,
	}, nil
}

// ExecError is returned (via the Done channel) when a docker exec
// process exits with a non-zero status.
type ExecError struct {
	Code int
}

func (e *ExecError) Error() string {
	return fmt.Sprintf("docker: exec exited %d", e.Code)
}

// StreamHandle provides streaming access to a running docker exec.
// The caller reads stdout/stderr via io.Reader; the demuxer runs
// in a background goroutine. The caller MUST call Close() when done
// (or on error) to release the exec resources.
type StreamHandle struct {
	Stdout io.ReadCloser // demuxed stdout stream
	Stderr io.ReadCloser // demuxed stderr stream
	Done   <-chan error   // closed when exec finishes (nil = success)
	cancel context.CancelFunc
}

// Close cancels the exec context and closes the stdout/stderr pipes.
// Safe to call multiple times.
func (h *StreamHandle) Close() {
	if h.cancel != nil {
		h.cancel()
	}
	if h.Stdout != nil {
		h.Stdout.Close()
	}
	if h.Stderr != nil {
		h.Stderr.Close()
	}
}

// ExecStream runs cmd inside the container and returns a StreamHandle
// for reading output as it arrives. Unlike Exec, it does not buffer
// the entire output — data flows through io.Pipe as Docker flushes
// each frame (typically 1–8 KB).
//
// The caller reads from Stdout in a loop; when Read returns io.EOF
// the exec has finished writing. Then read Done for the exit status.
func (d *Docker) ExecStream(
	ctx context.Context,
	containerID string,
	cmd []string,
	env []string,
) (*StreamHandle, error) {
	if len(cmd) == 0 {
		return nil, errors.New("docker: ExecStream: empty cmd")
	}

	execCtx, cancel := context.WithCancel(ctx)

	created, err := d.cli.ContainerExecCreate(execCtx, containerID, container.ExecOptions{
		Cmd:          cmd,
		Env:          env,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("docker: exec create: %w", err)
	}

	resp, err := d.cli.ContainerExecAttach(execCtx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		cancel()
		return nil, fmt.Errorf("docker: exec attach: %w", err)
	}

	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	done := make(chan error, 1)

	go func() {
		defer resp.Close()
		defer stdoutW.Close()
		defer stderrW.Close()

		// stdcopy.StdCopy demuxes Docker's multiplexed stream.
		// As Docker flushes frames, data flows to the pipes immediately.
		if _, err := stdcopy.StdCopy(stdoutW, stderrW, resp.Reader); err != nil && !errors.Is(err, io.EOF) {
			done <- fmt.Errorf("docker: exec demux: %w", err)
		}

		// Wait for exit code.
		for {
			info, inspErr := d.cli.ContainerExecInspect(execCtx, created.ID)
			if inspErr != nil {
				// Context cancelled (e.g. StreamHandle.Close) is normal.
				if execCtx.Err() != nil {
					close(done)
					return
				}
				done <- fmt.Errorf("docker: exec inspect: %w", inspErr)
				return
			}
			if !info.Running {
				if info.ExitCode != 0 {
					done <- &ExecError{Code: info.ExitCode}
				}
				close(done)
				return
			}
			select {
			case <-execCtx.Done():
				close(done)
				return
			case <-time.After(50 * time.Millisecond):
			}
		}
	}()

	return &StreamHandle{
		Stdout: stdoutR,
		Stderr: stderrR,
		Done:   done,
		cancel: cancel,
	}, nil
}

// Stop gracefully stops a container. Waits up to grace for the
// container's main process to exit, then sends SIGKILL.
func (d *Docker) Stop(ctx context.Context, containerID string, grace time.Duration) error {
	seconds := int(grace.Seconds())
	if err := d.cli.ContainerStop(ctx, containerID, container.StopOptions{
		Timeout: &seconds,
	}); err != nil {
		return fmt.Errorf("docker: stop %s: %w", containerID[:12], err)
	}
	return nil
}

// Remove deletes a container, forcing removal of a running one if
// necessary. Use Stop first when a graceful shutdown is appropriate.
func (d *Docker) Remove(ctx context.Context, containerID string) error {
	if err := d.cli.ContainerRemove(ctx, containerID, container.RemoveOptions{
		Force: true,
	}); err != nil {
		return fmt.Errorf("docker: remove %s: %w", containerID[:12], err)
	}
	return nil
}

// ListGopodContainers returns every container on the daemon that
// carries the gopod.chat label, running or not. Used by
// CleanupLeftovers at boot.
func (d *Docker) ListGopodContainers(ctx context.Context) ([]container.Summary, error) {
	list, err := d.cli.ContainerList(ctx, container.ListOptions{
		All: true,
		Filters: filters.NewArgs(
			filters.KeyValuePair{Key: "label", Value: LabelChat},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("docker: list gopod containers: %w", err)
	}
	return list, nil
}
