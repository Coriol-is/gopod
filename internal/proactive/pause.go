package proactive

import (
	"context"
	"encoding/json"
	"fmt"
)

// pauseMarker is the next_run PauseAll leaves on a task it paused. The
// scheduler ignores a paused task (and requires next_run > 0), and every other
// writer overwrites it: /tasks resume sets a real time, /tasks pause and
// Disable set 0. ResumeAll therefore reactivates only tasks still carrying it,
// and an owner's resume-then-pause during the global pause is never undone.
const pauseMarker int64 = -1

// ResumeReport says what ResumeAll did.
type ResumeReport struct {
	Resumed     []string // reactivated
	StillPaused []string // left paused because the current quiet hours leave no run time
	Skipped     []string // changed by the owner during the pause (or unusable), left as they are
}

// PauseAll is the bulk pause: it remembers which presets are active, pauses
// them with the pause marker, and refuses new Enable calls until ResumeAll,
// even when nothing was active ("off means off"). It is not a lock: the owner
// may still resume a task from /tasks, and Status reports the real task
// state. Calling it while already paused keeps the saved set.
func (s *Service) PauseAll(ctx context.Context) error {
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		if on, err := s.GloballyPaused(ctx); err != nil || on {
			return err
		}
		ids := []string{}
		for _, p := range Catalog() {
			task, ts, err := s.lookupTask(ctx, p)
			if err != nil {
				return err
			}
			if ts != taskLive || task.Status != "active" {
				continue
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", pauseMarker, task.LastRun); err != nil {
				return err
			}
			ids = append(ids, p.ID)
		}
		return s.saveGlobalSet(ctx, ids)
	})
}

// ResumeAll re-activates the presets PauseAll paused and that still carry its
// marker, then clears the pause. Each unedited task is re-rendered under the
// CURRENT zone, window and quiet hours first; one with no run time left stays
// paused. An owner-edited task is reactivated as written (owner decision: edits
// are not subject to quiet hours). With no pause on it does nothing.
func (s *Service) ResumeAll(ctx context.Context) (ResumeReport, error) {
	var rep ResumeReport
	err := s.inLockedTx(ctx, func(ctx context.Context) error {
		rep = ResumeReport{}
		ids, on, err := s.globalOff(ctx)
		if err != nil || !on {
			return err
		}
		env, err := s.env(ctx)
		if err != nil {
			return err
		}
		for _, id := range ids {
			p, ok := Lookup(id)
			if !ok {
				continue
			}
			task, ts, err := s.lookupTask(ctx, p)
			if err != nil {
				return err
			}
			if ts != taskLive || task.Status != "paused" || task.NextRun != pauseMarker {
				rep.Skipped = append(rep.Skipped, id)
				continue
			}
			row, err := s.loadRow(ctx, id)
			if err != nil {
				return err
			}
			set, err := decodeSettings(row, p)
			if err != nil {
				return err
			}
			if modified(row, task) {
				next, err := s.schedulerNext(task.ScheduleType, task.ScheduleValue)
				if err != nil { // unusable owner definition: leave it paused, unmarked
					if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
						return err
					}
					rep.Skipped = append(rep.Skipped, id)
					continue
				}
				if err = s.st.UpdateTaskStatus(ctx, task.ID, "active", next, task.LastRun); err != nil {
					return err
				}
				rep.Resumed = append(rep.Resumed, id)
				continue
			}
			prompt, sched, err := s.renderTask(p, set.Settings, env)
			if blocked(err) {
				if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
					return err
				}
				set.Reason = ReasonInsideQuiet
				row.Settings, row.UpdatedAt = encodeSettings(set), s.now().UnixMilli()
				if err = s.st.PutProactivePreset(ctx, row); err != nil {
					return err
				}
				rep.StillPaused = append(rep.StillPaused, id)
				continue
			}
			if err != nil {
				return fmt.Errorf("resuming %s: %w", id, err)
			}
			if err = s.rewrite(ctx, &task, prompt, sched); err != nil {
				return err
			}
			next, err := s.schedulerNext("cron", sched)
			if err != nil {
				return err
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "active", next, task.LastRun); err != nil {
				return err
			}
			set.Reason = ""
			if err = s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched)); err != nil {
				return err
			}
			rep.Resumed = append(rep.Resumed, id)
		}
		return s.st.DeleteProactiveSetting(ctx, keyGlobalOff)
	})
	return rep, err
}

func (s *Service) saveGlobalSet(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("proactive: encoding global pause set: %w", err)
	}
	return s.st.SetProactiveSetting(ctx, keyGlobalOff, string(b))
}

// dropFromGlobalSet removes id from the restore set while a pause is on, so an
// explicit Disable is not undone by ResumeAll.
func (s *Service) dropFromGlobalSet(ctx context.Context, id string) error {
	ids, on, err := s.globalOff(ctx)
	if err != nil || !on {
		return err
	}
	kept := []string{}
	for _, v := range ids {
		if v != id {
			kept = append(kept, v)
		}
	}
	if len(kept) == len(ids) {
		return nil
	}
	return s.saveGlobalSet(ctx, kept)
}
