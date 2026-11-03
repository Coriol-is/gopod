package proactive

import (
	"context"
	"fmt"
	"strings"

	"github.com/Coriol-is/gopod/internal/scheduler"
)

// Report says what a zone, window or quiet-hours change did to the presets.
type Report struct {
	Rescheduled []string // rewritten from the template under the new environment
	Kept        []string // owner-edited: prompt left alone (only CRON_TZ was moved)
	Paused      []string // paused because no run time is left outside quiet hours
}

// SetTimezone validates and stores the zone, then re-renders every live preset
// task under it (paused ones included, they stay paused). An owner-edited task
// keeps its prompt and cron fields and only has its CRON_TZ prefix swapped.
// Pending cal.reminder one-shots are absolute instants and are not touched.
func (s *Service) SetTimezone(ctx context.Context, tz string) (Report, error) {
	if err := ValidateTimezone(tz); err != nil {
		return Report{}, err
	}
	var rep Report
	err := s.inLockedTx(ctx, func(ctx context.Context) error {
		if err := s.st.SetProactiveSetting(ctx, keyTZ, tz); err != nil {
			return err
		}
		var err error
		rep, err = s.rerender(ctx, true)
		return err
	})
	if err != nil {
		return Report{}, err // rolled back: report nothing
	}
	return rep, nil
}

// SetWindow validates and stores the working window and re-renders pollers.
func (s *Service) SetWindow(ctx context.Context, w Window) error {
	if err := w.validate(); err != nil {
		return err
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		if err := s.st.SetProactiveSetting(ctx, keyWindow, fmt.Sprintf("%02d-%02d", w.StartHour, w.EndHour)); err != nil {
			return err
		}
		_, err := s.rerender(ctx, false)
		return err
	})
}

// SetQuiet stores the quiet window (nil turns quiet hours off) and re-renders.
// Presets with no run time left outside the window are paused with
// ReasonInsideQuiet or ReasonNoRunHours; nothing is shifted and nothing resumes by itself.
func (s *Service) SetQuiet(ctx context.Context, q *Quiet) error {
	if q != nil {
		if err := q.validate(); err != nil {
			return err
		}
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		var err error
		if q == nil {
			err = s.st.DeleteProactiveSetting(ctx, keyQuiet)
		} else {
			err = s.st.SetProactiveSetting(ctx, keyQuiet, fmt.Sprintf("%02d%02d-%02d%02d", q.StartMin/60, q.StartMin%60, q.EndMin/60, q.EndMin%60))
		}
		if err != nil {
			return err
		}
		_, err = s.rerender(ctx, false)
		return err
	})
}

// rerender applies the current environment to every live preset task.
func (s *Service) rerender(ctx context.Context, tzChanged bool) (Report, error) {
	var rep Report
	env, err := s.env(ctx)
	if err != nil {
		return rep, err
	}
	for _, p := range Catalog() {
		row, err := s.loadRow(ctx, p.ID)
		if err != nil {
			return rep, err
		}
		task, ts, err := s.lookupTask(ctx, p)
		if err != nil {
			return rep, err
		}
		if ts != taskLive {
			continue
		}
		if _, off := p.Unavailable(); off {
			continue // not rendered under a new zone, window or quiet hours
		}
		set, err := decodeSettings(row, p)
		if err != nil {
			return rep, err
		}

		if unmanaged(row) { // never ours: not rewritten, only reported
			if tzChanged {
				rep.Kept = append(rep.Kept, p.ID)
			}
			continue
		}
		if modified(row, task) {
			// The owner's edit stands: no quiet-hours or window rules apply to it.
			// A zone change moves only the CRON_TZ prefix of an edited cron; any
			// other schedule type, or text that would not parse, is left as is.
			if !tzChanged {
				continue
			}
			if task.ScheduleType == "cron" {
				sched := withTimezone(task.ScheduleValue, env.TZ)
				if sched != task.ScheduleValue && scheduler.ValidateCron(sched) == nil {
					if err = s.rewrite(ctx, &task, task.Prompt, sched); err != nil {
						return rep, err
					}
				}
			}
			rep.Kept = append(rep.Kept, p.ID)
			continue
		}

		prompt, sched, err := s.renderTask(p, set.Settings, env)
		if blocked(err) {
			reason := pauseReason(err)
			if task.Status != "active" {
				continue
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
				return rep, err
			}
			set.Reason = reason
			row.Settings, row.UpdatedAt = encodeSettings(set), s.now().UnixMilli()
			if err = s.st.PutProactivePreset(ctx, row); err != nil {
				return rep, err
			}
			rep.Paused = append(rep.Paused, p.ID)
			continue
		}
		if err != nil {
			return rep, fmt.Errorf("re-rendering %s: %w", p.ID, err)
		}
		if prompt == task.Prompt && sched == task.ScheduleValue && set.Reason == "" {
			continue
		}
		if err = s.rewrite(ctx, &task, prompt, sched); err != nil {
			return rep, err
		}
		set.Reason = ""
		if err = s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched)); err != nil {
			return rep, err
		}
		rep.Rescheduled = append(rep.Rescheduled, p.ID)
	}
	return rep, nil
}

// withTimezone sets the CRON_TZ prefix of a cron text.
func withTimezone(schedule, tz string) string {
	first, rest, ok := strings.Cut(schedule, " ")
	if ok && (strings.HasPrefix(first, "CRON_TZ=") || strings.HasPrefix(first, "TZ=")) {
		return "CRON_TZ=" + tz + " " + rest
	}
	return "CRON_TZ=" + tz + " " + schedule
}
