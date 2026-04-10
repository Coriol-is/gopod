package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// DefaultStopGrace is how long CleanupLeftovers gives a running
// leftover container to exit before forcing removal. Matches the 5s
// timeout in docs/ISOLATION.md §7.
const DefaultStopGrace = 5 * time.Second

// CleanupLeftovers walks every container with the picoclaw.chat label
// and decides what to do with each one. Called once at picoclaw
// startup before the runner starts spawning new containers for this
// process.
//
// Rules:
//
//   - Any picoclaw container tagged with a version label OTHER than
//     currentVersion is considered stale. New picoclaw build should not
//     trust state from the old one; stop and remove.
//   - Any picoclaw container whose state is already Exited or Dead is
//     removed regardless of version.
//   - A running container tagged with currentVersion is LEFT ALONE —
//     the new picoclaw process re-attaches to it as-is. This supports
//     hot-restarting picoclaw without interrupting in-flight agent
//     turns.
//
// Returns the number of containers cleaned up (stopped + removed).
func (d *Docker) CleanupLeftovers(ctx context.Context, currentVersion string) (int, error) {
	list, err := d.ListPicoclawContainers(ctx)
	if err != nil {
		return 0, err
	}

	var cleaned int
	for _, c := range list {
		chat := c.Labels[LabelChat]
		version := c.Labels[LabelVersion]
		running := c.State == "running"

		stale := version != currentVersion
		exited := !running

		// Running container at the current version is kept.
		if running && !stale {
			d.log.Info("leftover container kept (current version, running)",
				slog.String("chat", chat),
				slog.String("id", c.ID[:12]),
				slog.String("version", version))
			continue
		}

		// Everything else gets cleaned up.
		reason := "version mismatch"
		switch {
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
