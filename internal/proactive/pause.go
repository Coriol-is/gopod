package proactive

import (
	"context"
	"encoding/json"
	"fmt"
)

// PauseAll is the bulk pause: it remembers which presets are active, pauses
// them, and refuses new Enable calls until ResumeAll. It is not a lock: the
// owner may still resume a task from /tasks, and Status reports the real task
// state. Calling it while already paused keeps the saved set.
func (s *Service) PauseAll(ctx context.Context) error {
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		if s.GloballyPaused(ctx) {
			return nil
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
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
				return err
			}
			ids = append(ids, p.ID)
		}
		return s.saveGlobalSet(ctx, ids)
	})
}

// ResumeAll re-activates exactly the presets PauseAll paused and that are still
// paused, then clears the pause. With no pause on it does nothing.
func (s *Service) ResumeAll(ctx context.Context) error {
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		ids, on := s.globalOff(ctx)
		if !on {
			return nil
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
			if ts != taskLive || task.Status != "paused" {
				continue
			}
			next, err := s.schedulerNext(task.ScheduleType, task.ScheduleValue)
			if err != nil {
				return err
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "active", next, task.LastRun); err != nil {
				return err
			}
		}
		return s.st.DeleteProactiveSetting(ctx, keyGlobalOff)
	})
}

func (s *Service) saveGlobalSet(ctx context.Context, ids []string) error {
	b, err := json.Marshal(ids)
	if err != nil {
		return fmt.Errorf("proactive: encoding global pause set: %w", err)
	}
	return s.st.SetProactiveSetting(ctx, keyGlobalOff, string(b))
}

// dropFromGlobalSet removes id from the restore set while a pause is on, so an
// owner's explicit Disable is not undone by ResumeAll.
func (s *Service) dropFromGlobalSet(ctx context.Context, id string) error {
	ids, on := s.globalOff(ctx)
	if !on {
		return nil
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
