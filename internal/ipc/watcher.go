package ipc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// tickOnce sweeps every <DataDir>/ipc/<chat>/{messages,tasks} dir
// once. Errors are logged and never returned.
func (w *Watcher) tickOnce(ctx context.Context) {
	root := filepath.Join(w.cfg.DataDir, "ipc")
	entries, err := os.ReadDir(root)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.log.Warn("ipc: readdir root failed",
				"root", root, "err", err)
		}
		return
	}

	// Stable per-chat order so tests are deterministic.
	folders := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			folders = append(folders, e.Name())
		}
	}
	sort.Strings(folders)

	for _, folder := range folders {
		w.handleFolder(ctx, folder)
	}
}

// handleFolder sweeps a single chat's IPC channels: dispatches
// pending messages and prunes the .processed/.failed sidecars.
func (w *Watcher) handleFolder(ctx context.Context, folder string) {
	if w.handleFolderHook != nil {
		w.handleFolderHook(ctx, folder)
		return
	}
	w.processChannel(ctx, folder, "messages", w.processMessageFile)
	w.processChannel(ctx, folder, "tasks", w.processTaskFile)
	pruneDir(filepath.Join(w.cfg.DataDir, "ipc", folder, ".processed"), w.cfg.Retention)
	pruneDir(filepath.Join(w.cfg.DataDir, "ipc", folder, ".failed"), w.cfg.Retention)
}

// moveProcessed renames path to <folder>/.processed/<base>.
func (w *Watcher) moveProcessed(folder, path string) {
	dst := filepath.Join(w.cfg.DataDir, "ipc", folder, ".processed", filepath.Base(path))
	if err := os.Rename(path, dst); err != nil {
		w.log.Error("ipc: rename to .processed",
			"folder", folder, "path", path, "err", err)
		return
	}
	delete(w.retries, path)
}

// moveFailed renames path to <folder>/.failed/<base> and logs the cause.
func (w *Watcher) moveFailed(folder, path string, cause error) {
	dst := filepath.Join(w.cfg.DataDir, "ipc", folder, ".failed", filepath.Base(path))
	if err := os.Rename(path, dst); err != nil {
		w.log.Error("ipc: rename to .failed",
			"folder", folder, "path", path, "err", err)
		return
	}
	delete(w.retries, path)
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
	e := w.retries[path]
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
	dir := filepath.Join(w.cfg.DataDir, "ipc", folder, channel)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			w.log.Warn("ipc: readdir channel",
				"folder", folder, "channel", channel, "err", err)
		}
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
	for _, name := range names {
		path := filepath.Join(dir, name)
		if w.shouldSkipForBackoff(path) {
			continue
		}
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
