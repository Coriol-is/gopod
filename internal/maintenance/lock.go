// Package maintenance holds the host-side admission fence used by stopped-writer
// backups (CP-007). The fence is a kernel advisory lock on a file in the data
// directory: the controller holds it for its whole life and a capture holds it
// for the duration of the copy. The kernel releases it when its holder dies, so
// it is evidence of a live process, not of an in-memory flag, and it needs no
// cooperation from agents. The capture never stops anything: it only refuses.
package maintenance

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

// ControllerLockName is the lock file inside the data directory.
const ControllerLockName = ".controller.lock"

// ErrHeld means another process holds the lock.
var ErrHeld = errors.New("maintenance: lock is held by another process")

// ControllerLockPath returns the lock file path for a data directory.
func ControllerLockPath(dataDir string) string {
	return filepath.Join(dataDir, ControllerLockName)
}

// Lock is an exclusive advisory lock.
type Lock struct{ f *os.File }

// TryLock takes the exclusive lock without waiting. The file is created 0600
// and never followed through a symlink.
func TryLock(path string) (*Lock, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("maintenance: create lock dir: %w", err)
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return nil, fmt.Errorf("maintenance: open lock: %w", err)
	}
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = unix.Close(fd)
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrHeld
		}
		return nil, fmt.Errorf("maintenance: flock: %w", err)
	}
	return &Lock{f: os.NewFile(uintptr(fd), path)}, nil
}

// LockWait retries TryLock until the lock is free, wait elapses or ctx ends.
// The controller uses it at startup so a capture in progress delays the start
// instead of racing it.
func LockWait(ctx context.Context, path string, wait time.Duration) (*Lock, error) {
	deadline := time.Now().Add(wait)
	for {
		l, err := TryLock(path)
		if err == nil || !errors.Is(err, ErrHeld) {
			return l, err
		}
		if time.Now().After(deadline) {
			return nil, err
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}

// Release drops the lock. Safe on nil and repeated calls.
func (l *Lock) Release() error {
	if l == nil || l.f == nil {
		return nil
	}
	f := l.f
	l.f = nil
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
	return f.Close()
}
