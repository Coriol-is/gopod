package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/docker/docker/api/types/container"
	"golang.org/x/sys/unix"
)

type inspector interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
}

func controllerStopped(ctx context.Context, d inspector, name, data string) error {
	c, err := d.ContainerInspect(ctx, name)
	if err != nil || c.ContainerJSONBase == nil || c.State == nil || c.Config == nil || c.State.Running || c.State.Restarting || c.State.Paused || c.State.Pid != 0 || c.Config.Labels["com.docker.compose.service"] != "gopod" {
		return errUnavailable
	}
	if c.State.Status != "exited" && c.State.Status != "created" {
		return errUnavailable
	}
	for _, mount := range c.Mounts {
		if mount.Source == data && mount.Destination == data && mount.RW {
			return nil
		}
	}
	return errUnavailable
}
func regularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errUnavailable
	}
	return nil
}

// The lock coordinates concurrent admin invocations. Keeping the controller
// stopped for the entire operation remains an explicit deployment prerequisite;
// no advisory lock can stop an unrelated external SQLite writer.
func adminLock(data string, uid int) (func(), error) {
	if !filepath.IsAbs(data) || filepath.Clean(data) != data || uid <= 0 {
		return nil, errUnavailable
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(data, "/"), "/")
	for i, name := range parts {
		if name == "" {
			unix.Close(fd)
			return nil, errUnavailable
		}
		next, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return nil, errUnavailable
		}
		fd = next
		var stat unix.Stat_t
		if unix.Fstat(fd, &stat) != nil {
			unix.Close(fd)
			return nil, errUnavailable
		}
		final := i == len(parts)-1
		if stat.Uid != 0 && int(stat.Uid) != uid || final && int(stat.Uid) != uid || stat.Mode&0022 != 0 && (final || stat.Uid != 0 || stat.Mode&unix.S_ISVTX == 0) {
			unix.Close(fd)
			return nil, errUnavailable
		}
	}
	lock, err := unix.Openat(fd, ".group-admin.lock", unix.O_CREAT|unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	unix.Close(fd)
	if err != nil {
		return nil, errUnavailable
	}
	var stat unix.Stat_t
	if unix.Fstat(lock, &stat) != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || int(stat.Uid) != uid || stat.Mode&0077 != 0 || stat.Nlink != 1 {
		unix.Close(lock)
		return nil, errUnavailable
	}
	if unix.Flock(lock, unix.LOCK_EX|unix.LOCK_NB) != nil {
		unix.Close(lock)
		return nil, errUnavailable
	}
	return func() { _ = unix.Flock(lock, unix.LOCK_UN); _ = unix.Close(lock) }, nil
}
