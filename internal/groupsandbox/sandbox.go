// Package groupsandbox executes model-supplied commands outside native CLI
// credentials, with no network or host bind mounts. Only bounded regular-file
// snapshots cross into host-owned storage.
package groupsandbox

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-units"
)

const MaxOutput = 32768

var ErrUnavailable = errors.New("group tool unavailable")

type Executor struct {
	Client      *client.Client
	Root, Image string
	UID, GID    int
	mu          sync.Mutex
	locks       map[string]*sync.Mutex
}

func (e *Executor) lock(scope string) func() {
	e.mu.Lock()
	if e.locks == nil {
		e.locks = make(map[string]*sync.Mutex)
	}
	m := e.locks[scope]
	if m == nil {
		m = &sync.Mutex{}
		e.locks[scope] = m
	}
	e.mu.Unlock()
	m.Lock()
	return m.Unlock
}

func validScope(s string) bool {
	if len(s) < 8 || len(s) > 100 {
		return false
	}
	for _, c := range s {
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
			return false
		}
	}
	return true
}

// Run uses one disposable container per command. The caller binds scope from a
// host lease; it is never taken from JSON tool arguments.
func (e *Executor) Run(ctx context.Context, scope, command string) (string, error) {
	return e.RunChecked(ctx, scope, command, func(context.Context) error { return nil })
}
func (e *Executor) RunChecked(ctx context.Context, scope, command string, validate func(context.Context) error) (string, error) {
	return e.RunPinned(ctx, scope, command, "", validate)
}
func (e *Executor) RunPinned(ctx context.Context, scope, command, expectedImage string, validate func(context.Context) error) (output string, retErr error) {
	if !validScope(scope) || strings.TrimSpace(command) == "" || len(command) > 8192 || e.Client == nil || e.Image == "" || e.UID <= 0 || e.GID <= 0 {
		return "", ErrUnavailable
	}
	if validate == nil || validate(ctx) != nil {
		return "", ErrUnavailable
	}
	unlock := e.lock(scope)
	defer unlock()
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	// A previous interrupted/unknown cleanup must settle before another command.
	old, err := e.Client.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "gopod.group-tool="+scope))})
	if err != nil {
		return "", ErrUnavailable
	}
	for _, c := range old {
		if err = e.Client.ContainerRemove(ctx, c.ID, container.RemoveOptions{Force: true}); err != nil {
			return "", ErrUnavailable
		}
	}
	seed, err := e.snapshot(scope)
	if err != nil {
		return "", fmt.Errorf("load_snapshot: %w", ErrUnavailable)
	}
	image, _, err := e.Client.ImageInspectWithRaw(ctx, e.Image)
	if err != nil || expectedImage != "" && image.ID != expectedImage {
		return "", ErrUnavailable
	}
	suffix := make([]byte, 8)
	if _, err = rand.Read(suffix); err != nil {
		return "", ErrUnavailable
	}
	pids := int64(8)
	supervisor := 65534
	if e.UID == supervisor {
		supervisor = 65533
	}
	conf := &container.Config{Image: image.ID, User: strconv.Itoa(supervisor) + ":" + strconv.Itoa(supervisor), WorkingDir: "/", Entrypoint: []string{"sleep", "infinity"}, Env: []string{"HOME=/tmp", "PATH=/usr/local/bin:/usr/bin:/bin"}, Labels: map[string]string{"gopod.group-tool": scope}}
	host := &container.HostConfig{NetworkMode: "none", ReadonlyRootfs: true, CapDrop: []string{"ALL"}, SecurityOpt: []string{"no-new-privileges:true"}, Resources: container.Resources{Memory: 256 << 20, NanoCPUs: 500000000, PidsLimit: &pids, Ulimits: []*units.Ulimit{{Name: "nofile", Soft: 64, Hard: 64}, {Name: "core", Soft: 0, Hard: 0}, {Name: "memlock", Soft: 0, Hard: 0}, {Name: "fsize", Soft: 4 << 20, Hard: 4 << 20}}}, Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,noexec,size=16m,nr_inodes=256,mode=1777", "/workspace/chat": fmt.Sprintf("rw,nosuid,nodev,size=16m,nr_inodes=1024,mode=0700,uid=%d,gid=%d", e.UID, e.GID)}}
	created, err := e.Client.ContainerCreate(ctx, conf, host, nil, nil, "gopod-tool-"+hex.EncodeToString(suffix))
	if err != nil {
		return "", ErrUnavailable
	}
	defer func() {
		clean, done := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer done()
		_ = e.Client.ContainerUnpause(clean, created.ID)
		if err := e.Client.ContainerRemove(clean, created.ID, container.RemoveOptions{Force: true}); err != nil {
			retErr = ErrUnavailable
		}
	}()
	if err = e.Client.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		return "", ErrUnavailable
	}
	// Verify the kernel pids controller, not just Docker's requested config.
	// With the hard per-process address-space ceiling this also bounds aggregate
	// untrusted process memory on hosts where the memory cgroup is unavailable.
	probe, err := e.Client.ContainerExecCreate(ctx, created.ID, container.ExecOptions{User: e.user(), WorkingDir: "/", AttachStdout: true, AttachStderr: true, Cmd: []string{"sh", "-c", `limit=$(cat /sys/fs/cgroup/pids.max 2>/dev/null || cat /sys/fs/cgroup/pids/pids.max 2>/dev/null) && test "$limit" = 8`}})
	if err != nil {
		return "", ErrUnavailable
	}
	probeHandle, err := e.Client.ContainerExecAttach(ctx, probe.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", ErrUnavailable
	}
	stopProbe := context.AfterFunc(ctx, func() { probeHandle.Close() })
	_, probeErr := io.Copy(io.Discard, probeHandle.Reader)
	stopProbe()
	probeHandle.Close()
	if probeErr != nil || ctx.Err() != nil {
		return "", ErrUnavailable
	}
	checked, err := e.waitExec(ctx, probe.ID)
	if err != nil || checked.Running || checked.ExitCode != 0 {
		return "", fmt.Errorf("pids_verification: %w", ErrUnavailable)
	}
	if len(seed) > 0 {
		if err = e.hydrate(ctx, created.ID, seed); err != nil {
			return "", fmt.Errorf("hydrate_snapshot: %w", ErrUnavailable)
		}
	}
	exec, err := e.Client.ContainerExecCreate(ctx, created.ID, container.ExecOptions{User: e.user(), WorkingDir: "/workspace/chat", Cmd: []string{"sh", "-c", `ulimit -S -v 32768 && ulimit -H -v 32768 && exec sh -c "$1"`, "gopod-tool", command}, AttachStdout: true, AttachStderr: true})
	if err != nil {
		return "", ErrUnavailable
	}
	attached, err := e.Client.ContainerExecAttach(ctx, exec.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", ErrUnavailable
	}
	defer attached.Close()
	stop := context.AfterFunc(ctx, func() { attached.Close() })
	defer stop()
	bounded := &limitedBuffer{remaining: MaxOutput}
	if _, err = stdcopy.StdCopy(bounded, bounded, attached.Reader); err != nil || ctx.Err() != nil {
		return bounded.String(), ErrUnavailable
	}
	state, err := e.waitExec(ctx, exec.ID)
	if err != nil || state.Running {
		return "", ErrUnavailable
	}
	// The isolated supervisor has a different non-root uid, so model commands
	// cannot turn it into a hidden writer. Reject all remaining actor processes
	// before the trusted archive reader starts; background jobs never persist.
	top, err := e.Client.ContainerTop(ctx, created.ID, []string{"-eo", "pid"})
	if err != nil || len(top.Processes) != 1 {
		return bounded.String(), fmt.Errorf("background_process: %w", ErrUnavailable)
	}
	if ctx.Err() != nil || validate(ctx) != nil {
		return "", ErrUnavailable
	}
	if err = e.export(ctx, created.ID, scope, validate); err != nil {
		return bounded.String(), fmt.Errorf("snapshot_export: %w", ErrUnavailable)
	}
	if state.ExitCode != 0 {
		return bounded.String(), fmt.Errorf("group command exited %d", state.ExitCode)
	}
	return bounded.String(), nil
}

type limitedBuffer struct {
	bytes.Buffer
	remaining int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if len(p) > b.remaining {
		p = p[:b.remaining]
	}
	_, _ = b.Buffer.Write(p)
	b.remaining -= len(p)
	return n, nil
}

func privateDir(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return ErrUnavailable
	}
	return nil
}

// The root must have been created under protected host roots, never supplied by
// model input or placed beneath any writable bind source.
func (e *Executor) scopeDir(scope string) (string, error) {
	if !filepath.IsAbs(e.Root) || privateDir(e.Root) != nil {
		return "", ErrUnavailable
	}
	p := filepath.Join(e.Root, scope)
	if err := os.Mkdir(p, 0700); err != nil && !os.IsExist(err) {
		return "", err
	}
	if err := privateDir(p); err != nil {
		return "", err
	}
	return p, nil
}

var _ io.Writer = (*limitedBuffer)(nil)

func (e *Executor) user() string { return strconv.Itoa(e.UID) + ":" + strconv.Itoa(e.GID) }
