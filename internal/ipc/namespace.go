package ipc

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"syscall"
)

func (w *Watcher) openNamespace(folder string) (*os.Root, error) {
	return os.OpenRoot(filepath.Join(w.cfg.DataDir, "ipc", folder))
}

func (w *Watcher) readTask(path, folder string) ([]byte, error) {
	root, err := w.openNamespace(folder)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	name := filepath.Join("tasks", filepath.Base(path))
	info, err := root.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular JSON task file required")
	}
	f, err := root.OpenFile(name, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular JSON task file required")
	}

	return io.ReadAll(io.LimitReader(f, (64<<10)+1))
}

func (w *Watcher) archiveFile(folder, path, destination string) error {
	root, err := w.openNamespace(folder)
	if err != nil {
		return err
	}
	defer root.Close()
	if err = root.MkdirAll(destination, 0o700); err != nil {
		return err
	}
	channel := filepath.Base(filepath.Dir(path))
	if channel != "messages" && channel != "tasks" {
		return fmt.Errorf("invalid IPC source channel")
	}
	return root.Rename(filepath.Join(channel, filepath.Base(path)), filepath.Join(destination, filepath.Base(path)))
}

func (w *Watcher) pruneNamespace(folder string) {
	root, err := w.openNamespace(folder)
	if err != nil {
		return
	}
	defer root.Close()
	for _, channel := range []string{".processed", ".failed", "results"} {
		f, err := root.OpenFile(channel, os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
		if err != nil {
			continue
		}
		info, statErr := f.Stat()
		if statErr != nil || !info.IsDir() {
			f.Close()
			continue
		}
		entries, err := f.ReadDir(-1)
		f.Close()
		if err != nil {
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
			if err = root.Remove(filepath.Join(channel, names[0])); err != nil {
				w.log.Warn("ipc: prune receipt/archive", "folder", folder, "channel", channel, "err", err)
			}
			names = names[1:]
		}
	}
}
