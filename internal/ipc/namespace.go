package ipc

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"
)

func (w *Watcher) namespacePath(folder string) (string, error) {
	if w.cfg.NamespacePath != nil {
		path, ok := w.cfg.NamespacePath(folder)
		if !ok || !filepath.IsAbs(path) || filepath.Clean(path) != path {
			return "", fmt.Errorf("IPC namespace unavailable")
		}
		return path, nil
	}
	if folder == "" || filepath.Base(folder) != folder || folder == "." || folder == ".." {
		return "", fmt.Errorf("IPC namespace unavailable")
	}
	return filepath.Join(w.cfg.DataDir, "ipc", folder), nil
}
func (w *Watcher) openNamespace(folder string) (*os.Root, error) {
	path, err := w.namespacePath(folder)
	if err != nil {
		return nil, err
	}
	return os.OpenRoot(path)
}

func (w *Watcher) readTask(path, folder string) ([]byte, error) {
	return w.readRequest(path, folder, "tasks")
}

func (w *Watcher) readRequest(path, folder, channel string) ([]byte, error) {
	root, err := w.openNamespace(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	dir, err := openDirectory(root, channel, false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	fd, err := unix.Openat(int(dir.Fd()), filepath.Base(path), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), filepath.Base(path))
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular JSON request file required")
	}

	return io.ReadAll(io.LimitReader(f, (64<<10)+1))
}

// openDirectory pins each path component before walking the next one. os.Root
// contains paths, but intentionally permits symlinks within the root; IPC channel
// separation additionally requires that no component be a symlink.
func openDirectory(root *os.Root, path string, create bool) (*os.File, error) {
	if path == "" || filepath.Clean(path) != path || filepath.IsAbs(path) {
		return nil, fmt.Errorf("invalid IPC directory")
	}
	dir, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	for _, part := range strings.Split(path, string(filepath.Separator)) {
		if part == "." || part == ".." {
			dir.Close()
			return nil, fmt.Errorf("invalid IPC directory")
		}
		if create {
			err = unix.Mkdirat(int(dir.Fd()), part, 0700)
			if err != nil && !errors.Is(err, unix.EEXIST) {
				dir.Close()
				return nil, err
			}
		}
		fd, err := unix.Openat(int(dir.Fd()), part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		dir.Close()
		if err != nil {
			return nil, err
		}
		dir = os.NewFile(uintptr(fd), part)
	}
	return dir, nil
}

func (w *Watcher) archiveFile(folder, path, destination string) error {
	if destination != ".processed" && destination != ".failed" {
		return fmt.Errorf("invalid IPC archive")
	}
	channel := filepath.Base(filepath.Dir(path))
	if channel != "messages" && channel != "tasks" {
		return fmt.Errorf("invalid IPC source channel")
	}
	root, err := w.openNamespace(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	src, err := openDirectory(root, channel, false)
	if err != nil {
		return err
	}
	defer src.Close()
	dst, err := openDirectory(root, destination, true)
	if err != nil {
		return err
	}
	defer dst.Close()
	si, err := src.Stat()
	if err != nil {
		return err
	}
	di, err := dst.Stat()
	if err != nil {
		return err
	}
	if os.SameFile(si, di) {
		return fmt.Errorf("IPC archive aliases source")
	}
	name := filepath.Base(path)
	if err := unix.Renameat(int(src.Fd()), name, int(dst.Fd()), name); err != nil {
		return err
	}
	var st unix.Stat_t
	if err := unix.Fstatat(int(src.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); !errors.Is(err, unix.ENOENT) {
		return fmt.Errorf("IPC source still present after archive")
	}
	return nil
}

func (w *Watcher) pruneNamespace(folder string) {
	root, err := w.openNamespace(folder)
	if err != nil {
		return
	}
	defer root.Close()
	for _, channel := range []string{".processed", ".failed", "results", "results/messages"} {
		f, err := openDirectory(root, channel, false)
		if err != nil {
			continue
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.IsDir() {
			f.Close()
			continue
		}
		entries, err := f.ReadDir(maxDirectoryEntries)
		defer f.Close()
		if err != nil && !errors.Is(err, io.EOF) {
			continue
		}
		var names []string
		for _, e := range entries {
			if !e.IsDir() {
				names = append(names, e.Name())
			}
		}
		sort.Strings(names)
		for len(names) > w.cfg.Retention {
			if err = unix.Unlinkat(int(f.Fd()), names[0], 0); err != nil {
				w.log.Warn("ipc: prune receipt/archive", "folder", folder, "channel", channel, "err", err)
			}
			names = names[1:]
		}
	}
}
