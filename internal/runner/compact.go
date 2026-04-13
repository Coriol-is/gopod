package runner

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/spaceinvaderz/gopod/internal/runner/mountsec"
)

// CompactConfig holds the three compact trigger strategies.
type CompactConfig struct {
	After    int           // compact after N turns (0 = disabled)
	Interval time.Duration // compact every N duration (0 = disabled)
	TimeOfDay string       // compact at HH:MM daily ("" = disabled)
}

// StartCompactWatcher launches a background goroutine that checks
// interval and daily-time triggers. Turn-based triggers are handled
// inline in Runner.Run(). This watcher handles the time-based ones.
func (r *Runner) StartCompactWatcher(ctx context.Context, cfg CompactConfig, allowlist *mountsec.Allowlist) {
	if cfg.Interval <= 0 && cfg.TimeOfDay == "" {
		return // nothing to watch
	}

	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()

		r.log.Info("compact watcher started",
			slog.Duration("interval", cfg.Interval),
			slog.String("time_of_day", cfg.TimeOfDay))

		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				r.checkTimeBasedCompact(ctx, now, cfg, allowlist)
			}
		}
	}()
}

func (r *Runner) checkTimeBasedCompact(ctx context.Context, now time.Time, cfg CompactConfig, allowlist *mountsec.Allowlist) {
	for _, chatFolder := range r.ActiveChats() {
		if r.getTurnCount(chatFolder) == 0 {
			continue // nothing to compact
		}

		shouldCompact := false
		reason := ""

		// Interval check.
		if cfg.Interval > 0 {
			last := r.getLastCompactTime(chatFolder)
			if !last.IsZero() && now.Sub(last) >= cfg.Interval {
				shouldCompact = true
				reason = "interval"
			} else if last.IsZero() && r.getTurnCount(chatFolder) > 0 {
				// First compact: set the baseline.
				r.setLastCompactTime(chatFolder, now)
			}
		}

		// Daily time check.
		if !shouldCompact && cfg.TimeOfDay != "" {
			target, err := time.Parse("15:04", cfg.TimeOfDay)
			if err == nil {
				todayTarget := time.Date(now.Year(), now.Month(), now.Day(),
					target.Hour(), target.Minute(), 0, 0, now.Location())
				// Fire if we're within 1 minute of the target time.
				if now.After(todayTarget) && now.Sub(todayTarget) < 2*time.Minute {
					last := r.getLastCompactTime(chatFolder)
					if last.Before(todayTarget) {
						shouldCompact = true
						reason = "daily"
					}
				}
			}
		}

		if shouldCompact {
			r.log.Info("time-based compact triggered",
				slog.String("chat", chatFolder),
				slog.String("reason", reason))
			r.CompactSession(ctx, chatFolder, TierRegistered, allowlist)
			r.setLastCompactTime(chatFolder, now)
		}
	}
}

func (r *Runner) getLastCompactTime(chatFolder string) time.Time {
	r.activityMu.Lock()
	t, ok := r.lastCompact[chatFolder]
	r.activityMu.Unlock()
	if ok {
		return t
	}
	// Try loading from persistent store.
	if r.store != nil {
		v, _ := r.store.GetState(context.Background(), "compact_at:"+chatFolder)
		if v != "" {
			var ms int64
			fmt.Sscanf(v, "%d", &ms)
			if ms > 0 {
				t = time.UnixMilli(ms)
				r.activityMu.Lock()
				if r.lastCompact == nil {
					r.lastCompact = make(map[string]time.Time)
				}
				r.lastCompact[chatFolder] = t
				r.activityMu.Unlock()
				return t
			}
		}
	}
	return time.Time{}
}

func (r *Runner) setLastCompactTime(chatFolder string, t time.Time) {
	r.activityMu.Lock()
	if r.lastCompact == nil {
		r.lastCompact = make(map[string]time.Time)
	}
	r.lastCompact[chatFolder] = t
	r.activityMu.Unlock()
	// Persist to store for surviving restarts.
	if r.store != nil {
		r.store.SetState(context.Background(), "compact_at:"+chatFolder,
			fmt.Sprintf("%d", t.UnixMilli()))
	}
}
