package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"
)

// DefaultStopGrace is how long CleanupLeftovers gives a running
// leftover container to exit before forcing removal. Matches the 5s
// timeout in docs/ISOLATION.md §7.
const DefaultStopGrace = 5 * time.Second

// DefaultIdleTimeout is how long a per-chat container can sit
// without any agent activity before the idle watcher stops and
// removes it. Matches docs/ISOLATION.md §7.2 and the
// GOPOD_IDLE_TIMEOUT default.
const DefaultIdleTimeout = 30 * time.Minute

// idleTickInterval is how often the idle watcher checks every chat's
// LastActivity. Much shorter than the timeout so a kill happens
// promptly after a chat goes idle.
const idleTickInterval = 1 * time.Minute

// CleanupLeftovers walks every container with the gopod.chat label
// and decides what to do with each one. Called once at gopod
// startup before the runner starts spawning new containers for this
// process.
//
// Rules:
//
//   - currentVersion == "dev": EVERY gopod container is removed,
//     running or not. Dev builds (`go run` and untagged `go build`)
//     all share version=dev, so version comparison is useless for
//     them — gopod cannot tell whether a leftover is from a build
//     with the same source as the current one. Removing all of them
//     is the only safe option, and it lets `gopod restart` pick
//     up code changes without an explicit `docker rm` step.
//   - Any gopod container tagged with a version label OTHER than
//     currentVersion is considered stale. New gopod build should not
//     trust state from the old one; stop and remove.
//   - Any gopod container whose state is already Exited or Dead is
//     removed regardless of version.
//   - A running container tagged with currentVersion (and currentVersion
//     is not "dev") is LEFT ALONE — the new gopod process re-attaches
//     to it as-is. This supports hot-restarting a tagged release build
//     without interrupting in-flight agent turns.
//
// Returns the number of containers cleaned up (stopped + removed).
func (d *Docker) CleanupLeftovers(ctx context.Context, currentVersion string) (int, error) {
	list, err := d.ListGopodContainers(ctx)
	if err != nil {
		return 0, err
	}

	devMode := currentVersion == "dev"

	var cleaned int
	for _, c := range list {
		chat := c.Labels[LabelChat]
		version := c.Labels[LabelVersion]
		running := c.State == "running"

		stale := version != currentVersion
		exited := !running

		// Running container at the current TAGGED version is kept so a
		// hot-restart of gopod doesn't interrupt in-flight agent
		// turns. Dev builds skip this branch — see the package comment.
		if running && !stale && !devMode {
			d.log.Info("leftover container kept (current version, running)",
				slog.String("chat", chat),
				slog.String("id", c.ID[:12]),
				slog.String("version", version))
			continue
		}

		// Everything else gets cleaned up.
		reason := "version mismatch"
		switch {
		case devMode:
			reason = "dev build (version=dev cannot be trusted across restarts)"
		case exited && stale:
			reason = "exited + version mismatch"
		case exited && !stale:
			reason = "exited"
		case running && stale:
			reason = "running but wrong version"
		}

		d.log.Info("cleaning up leftover container",
			slog.String("chat", chat),
			slog.String("id", c.ID[:12]),
			slog.String("reason", reason),
			slog.String("state", c.State))

		if running {
			if err := d.Stop(ctx, c.ID, DefaultStopGrace); err != nil {
				// Log and keep going — we still want to try to remove.
				d.log.Warn("stop failed during cleanup",
					slog.String("id", c.ID[:12]),
					slog.Any("err", err))
			}
		}
		if err := d.Remove(ctx, c.ID); err != nil {
			return cleaned, fmt.Errorf("cleanup: remove %s: %w", c.ID[:12], err)
		}
		cleaned++
	}
	return cleaned, nil
}

// StartIdleWatcher launches a single background goroutine that checks
// every active chat's LastActivity once per idleTickInterval. Any
// chat whose container has been idle for longer than timeout is
// stopped, removed, and dropped from the activity map.
//
// The watcher exits when ctx is cancelled. Caller is responsible for
// draining the goroutine via ctx — gopod's main.go does this by
// passing the SIGINT-aware context.
//
// One watcher per Runner is sufficient: the per-chat per-tick scan is
// O(active chats) which is tiny on personal-assistant scale, and a
// single goroutine sidesteps the race a per-chat watcher would have
// with EnsureRunning re-spawning a chat right as its watcher fires.
//
// In-flight prompts are NOT specifically protected: if a single
// prompt takes longer than the idle timeout, the watcher will kill
// the container mid-flight. The default 30-minute timeout is far
// longer than any reasonable claude -p invocation, so this is
// acceptable for v0; a heartbeat refresh during long prompts can
// land in a follow-up if it ever matters.
func (r *Runner) StartIdleWatcher(ctx context.Context, timeout time.Duration) {
	if timeout <= 0 {
		timeout = DefaultIdleTimeout
	}
	go r.idleWatcherLoop(ctx, timeout)
}

func (r *Runner) idleWatcherLoop(ctx context.Context, timeout time.Duration) {
	r.log.Info("idle watcher started",
		slog.Duration("timeout", timeout),
		slog.Duration("tick", idleTickInterval))

	t := time.NewTicker(idleTickInterval)
	defer t.Stop()

	for {
		select {
		case <-ctx.Done():
			r.log.Info("idle watcher stopped")
			return
		case now := <-t.C:
			r.idleWatcherSweep(ctx, now, timeout)
		}
	}
}

// idleWatcherSweep is one pass over the active chats map. Extracted
// so it can be unit-tested without spinning up a real time.Ticker.
func (r *Runner) idleWatcherSweep(ctx context.Context, now time.Time, timeout time.Duration) {
	for _, chatFolder := range r.ActiveChats() {
		last := r.LastActivity(chatFolder)
		if last.IsZero() {
			continue
		}
		idle := now.Sub(last)
		if idle < timeout {
			continue
		}
		// Per-chat lock so we don't race a fresh Ensure call that
		// just spawned the container at the very moment we decided
		// to kill it.
		r.lockChat(chatFolder)
		// Re-check after acquiring the lock — the timestamp may have
		// been refreshed by a Run() that was waiting on the lock.
		last = r.LastActivity(chatFolder)
		if !last.IsZero() && now.Sub(last) >= timeout {
			r.killIdleChat(ctx, chatFolder)
		}
		r.unlockChat(chatFolder)
	}
}

// killIdleChat stops + removes the container for chatFolder and
// drops the chat from the activity map. Logged at info; failures
// are logged at warn but otherwise swallowed (we'll try again next
// tick).
func (r *Runner) killIdleChat(ctx context.Context, chatFolder string) {
	name := ContainerName(chatFolder)
	r.log.Info("idle: stopping container",
		slog.String("chat", chatFolder),
		slog.String("name", name))

	id, err := r.d.inspectByName(ctx, name)
	if err != nil {
		r.log.Warn("idle: inspect failed",
			slog.String("chat", chatFolder),
			slog.Any("err", err))
		// Drop from activity map so we stop scanning a chat we
		// can't even find.
		r.forgetChat(chatFolder)
		return
	}
	if id == "" {
		// Container is already gone (operator killed it manually,
		// previous gopod run cleaned it up, etc). Just forget.
		r.forgetChat(chatFolder)
		return
	}

	if err := r.d.Stop(ctx, id, DefaultStopGrace); err != nil {
		r.log.Warn("idle: stop failed",
			slog.String("chat", chatFolder),
			slog.Any("err", err))
		// Don't forget — try again next tick.
		return
	}
	if err := r.d.Remove(ctx, id); err != nil {
		r.log.Warn("idle: remove failed",
			slog.String("chat", chatFolder),
			slog.Any("err", err))
		return
	}
	r.forgetChat(chatFolder)
	r.log.Info("idle: container removed",
		slog.String("chat", chatFolder))
}

// forgetChat drops a chat from the activity map. Used after the chat
// has been killed by the idle watcher; a future Run() call for the
// same chat will re-spawn the container and re-add it to activity
// via touch().
func (r *Runner) forgetChat(chatFolder string) {
	r.activityMu.Lock()
	delete(r.activity, chatFolder)
	r.activityMu.Unlock()
}

// errIdleWatcherShutdown is the sentinel returned if a runner method
// is called after the watcher has been shut down. Currently unused —
// reserved for future Stop() semantics.
var errIdleWatcherShutdown = errors.New("runner: idle watcher shut down")

var _ = errIdleWatcherShutdown // future-use placeholder
