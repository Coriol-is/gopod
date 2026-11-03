package ipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
)

// tickOnce sweeps every <DataDir>/ipc/<chat>/{messages,tasks} dir
// once. Errors are logged and never returned.
func (w *Watcher) tickOnce(ctx context.Context) {
	w.beginSweep()
	w.pruneWatcherState()
	var folders []string
	if w.cfg.Namespaces != nil {
		var err error
		folders, err = w.cfg.Namespaces(ctx)
		if err != nil {
			w.log.Warn("ipc: namespace inventory unavailable")
			return
		}
	} else {
		root := filepath.Join(w.cfg.DataDir, "ipc")
		entries, err := os.ReadDir(root)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				w.log.Warn("ipc: readdir root failed", "err", err)
			}
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				folders = append(folders, e.Name())
			}
		}
	}
	sort.Strings(folders)
	w.maybeReconcileArtifacts(ctx)

	last := ""
	for _, folder := range folders {
		if folder == last {
			continue
		}
		last = folder
		w.handleFolder(ctx, folder)
	}
}

// handleFolder sweeps a single chat's IPC channels: dispatches
// pending messages and prunes the .processed/.failed sidecars.
func (w *Watcher) handleFolder(ctx context.Context, folder string) {
	if w.cfg.NamespaceAllowed != nil && !w.cfg.NamespaceAllowed(ctx, folder) {
		return
	}
	w.artifactPreps = 0
	if w.handleFolderHook != nil {
		w.handleFolderHook(ctx, folder)
		return
	}
	w.processChannel(ctx, folder, "messages", w.processMessageFile)
	w.processChannel(ctx, folder, "tasks", w.processTaskFile)
	w.reconcileDurableReceipts(ctx, folder)
	w.pruneNamespace(folder)
}

// moveProcessed renames path to <folder>/.processed/<base>.
func (w *Watcher) moveProcessed(folder, path string) bool {
	if err := w.archiveFile(folder, path, ".processed"); err != nil {
		w.log.Error("ipc: rename to .processed",
			"folder", folder, "path", path, "err", err)
		return false
	}
	delete(w.retries, path)
	delete(w.writeFloor, path)
	return true
}

// moveFailed renames path to <folder>/.failed/<base> and logs the cause.
func (w *Watcher) moveFailed(folder, path string, cause error) {
	if err := w.archiveFile(folder, path, ".failed"); err != nil {
		w.log.Error("ipc: rename to .failed",
			"folder", folder, "path", path, "err", err)
		return
	}
	delete(w.retries, path)
	delete(w.writeFloor, path)
	w.log.Warn("ipc: file moved to .failed",
		"folder", folder, "file", filepath.Base(path), "cause", cause)
}

// bumpRetry records one transient failure for path. Returns true if
// the retry budget is exhausted (caller should send to .failed/).
//
// Semantics: cfg.MaxRetries is the total number of dispatch attempts
// allowed per file. cfg.RetryBackoff is the wait between attempts —
// RetryBackoff[i] is the cool-down before attempt i+1. With the
// default MaxRetries=3 and RetryBackoff=[5s, 15s, 45s], only the
// first two backoff entries are used (the 45s is reserved for a
// future MaxRetries bump).
func (w *Watcher) bumpRetry(path string) (exhausted bool) {
	e, exists := w.retries[path]
	if !exists && len(w.retries) >= maxWatcherEntries {
		return true
	}
	e.attempts++
	if e.attempts >= w.cfg.MaxRetries {
		return true
	}
	idx := e.attempts - 1
	if idx >= len(w.cfg.RetryBackoff) {
		idx = len(w.cfg.RetryBackoff) - 1
	}
	e.nextRetryAt = w.cfg.NowFn().Add(w.cfg.RetryBackoff[idx])
	w.retries[path] = e
	return false
}

// shouldSkipForBackoff returns true when path is in retry cool-down.
func (w *Watcher) shouldSkipForBackoff(path string) bool {
	e, ok := w.retries[path]
	if !ok {
		return false
	}
	return w.cfg.NowFn().Before(e.nextRetryAt)
}

// processChannel walks one channel dir and dispatches each *.json
// through handler. Files in retry cool-down are skipped.
func (w *Watcher) processChannel(ctx context.Context, folder, channel string,
	handler func(ctx context.Context, folder, path string) bool,
) {
	base, err := w.namespacePath(folder)
	if err != nil {
		return
	}
	dir := filepath.Join(base, channel)
	// Never enumerate a path first and validate its content later: filenames
	// themselves are private metadata. The rooted open prevents traversal out
	// of the namespace, and NOFOLLOW rejects a substituted channel symlink
	// (including one pointing to another directory inside this namespace).
	root, err := w.openNamespace(folder)
	if err != nil {
		return
	}
	defer root.Close()
	f, err := openDirectory(root, channel, false)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.log.Warn("ipc: unsafe or unreadable channel", "folder", folder, "channel", channel)
		}
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.IsDir() {
		w.log.Warn("ipc: channel must be a directory", "folder", folder, "channel", channel)
		return
	}
	// A broken archive must not allow repeated side effects, even across restarts.
	for _, archive := range []string{".processed", ".failed"} {
		dst, err := openDirectory(root, archive, true)
		if err != nil {
			w.log.Warn("ipc: unsafe archive; channel deferred", "folder", folder)
			return
		}
		dst.Close()
	}
	entries, err := f.ReadDir(maxDirectoryEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		w.log.Warn("ipc: readdir channel failed", "folder", folder, "channel", channel)
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if filepath.Ext(e.Name()) != ".json" {
			continue
		}
		names = append(names, e.Name())
	}
	sort.Strings(names)
	dispatched := 0
	for _, name := range names {
		if ctx.Err() != nil || dispatched >= maxChannelDispatches {
			break
		}
		path := filepath.Join(dir, name)
		if w.shouldSkipForBackoff(path) {
			continue
		}
		dispatched++
		w.safeDispatch(ctx, folder, path, handler)
	}
}

// safeDispatch wraps handler in a recover so a panic on one file
// cannot kill the watcher loop.
func (w *Watcher) safeDispatch(ctx context.Context, folder, path string,
	handler func(ctx context.Context, folder, path string) bool,
) {
	defer func() {
		if r := recover(); r != nil {
			w.log.Error("ipc: dispatch panic",
				"folder", folder, "file", filepath.Base(path), "panic", r)
			w.moveFailed(folder, path, fmt.Errorf("panic: %v", r))
		}
	}()
	handler(ctx, folder, path)
}

// Bound work and state independently of the number of files an agent creates.
// Backlogs remain on disk for subsequent ticks; cache eviction never replays a
// terminal delivery. A full safety-state map refuses new work rather than
// discarding the old outcome or weakening capability timing.
const (
	maxDirectoryEntries  = 1024
	maxChannelDispatches = 32
	maxWatcherEntries    = 4096
)

func (w *Watcher) pruneWatcherState() {
	for path := range w.retries {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			delete(w.retries, path)
		}
	}
	for path := range w.writeFloor {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			delete(w.writeFloor, path)
		}
	}
	for path := range w.terminalMessages {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			delete(w.terminalMessages, path)
		}
	}
}
