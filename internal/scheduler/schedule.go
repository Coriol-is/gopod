package scheduler

import (
	"fmt"
	"time"
	_ "time/tzdata" // IANA zones must work in the minimal runtime image.
)

// FirstRunAt computes an initial schedule slot without calling a model. Cron
// without CRON_TZ uses the process timezone; explicit IANA zones handle DST.
func FirstRunAt(scheduleType, value string, now time.Time) (int64, error) {
	switch scheduleType {
	case "cron":
		schedule, err := cronParser.Parse(value)
		if err != nil {
			return 0, fmt.Errorf("invalid cron: %w", err)
		}
		next := schedule.Next(now)
		if next.IsZero() {
			return 0, fmt.Errorf("cron has no future occurrence")
		}
		return next.UnixMilli(), nil
	case "interval":
		if err := ValidateInterval(value); err != nil {
			return 0, err
		}
		d, _ := time.ParseDuration(value)
		return now.Add(d).UnixMilli(), nil
	case "once":
		at, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return 0, fmt.Errorf("once requires RFC3339 with timezone: %w", err)
		}
		if !at.After(now) {
			return 0, fmt.Errorf("once must be in the future")
		}
		return at.UnixMilli(), nil
	default:
		return 0, fmt.Errorf("unknown scheduleType %q: use cron, interval or once", scheduleType)
	}
}
