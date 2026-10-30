package proactive

import (
	"context"
	"fmt"

	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

// State is the externally visible state of a preset.
type State string

const (
	StateOff    State = "off"    // no live task
	StateActive State = "active" // task active
	StatePaused State = "paused" // task exists but paused
)

// Status describes one preset for a screen.
type Status struct {
	Preset           Preset
	State            State
	Settings         Settings
	Modified         bool   // the task was edited outside gopod
	TemplateOutdated bool   // the stored template version is older than the catalog's
	PausedReason     string // set when gopod paused the task itself
	GloballyPaused   bool
	Conflict         bool // a foreign task holds the preset's id; it is never touched
	TaskID           string
	NextRun          int64
	LastRun          int64
}

// Status reports the real task state of one preset.
func (s *Service) Status(ctx context.Context, id string) (Status, error) {
	p, ok := Lookup(id)
	if !ok {
		return Status{}, ErrUnknownPreset
	}
	row, err := s.loadRow(ctx, id)
	if err != nil {
		return Status{}, err
	}
	t, ts, err := s.lookupTask(ctx, p)
	if err != nil {
		return Status{}, err
	}
	set, err := decodeSettings(row, p)
	if err != nil {
		return Status{}, err
	}
	gp, err := s.GloballyPaused(ctx)
	if err != nil {
		return Status{}, err
	}
	st := Status{Preset: p, State: StateOff, Settings: set.Settings, GloballyPaused: gp, Conflict: ts == taskForeign}
	if ts != taskLive {
		return st, nil
	}
	st.TaskID, st.NextRun, st.LastRun = t.ID, t.NextRun, t.LastRun
	st.State = StateActive
	if t.Status == "paused" {
		st.State = StatePaused
		st.PausedReason = set.Reason
	}
	st.Modified = modified(row, t)
	st.TemplateOutdated = row.TemplateVersion < p.TemplateVersion
	return st, nil
}

// List reports every catalog preset in display order.
func (s *Service) List(ctx context.Context) ([]Status, error) {
	var out []Status
	for _, p := range Catalog() {
		st, err := s.Status(ctx, p.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, nil
}

// inLockedTx runs fn holding the task lock inside one store transaction.
// scheduler.WithTaskLock honors the lock key of the IPC path, so a caller that
// is already inside ProcessTaskRequest does not deadlock.
func (s *Service) inLockedTx(ctx context.Context, fn func(ctx context.Context) error) error {
	return scheduler.WithTaskLock(ctx, s.st, func(ctx context.Context) error {
		return s.st.InTaskTx(ctx, fn)
	})
}

// Enable switches a preset on: it creates the deterministic task, or
// reactivates the existing one, rewriting its prompt and schedule only when the
// owner has not edited it. Callers decide whether the connector is ready; this
// layer only enforces the rules about tasks. Idempotent.
func (s *Service) Enable(ctx context.Context, id string) error {
	p, ok := Lookup(id)
	if !ok {
		return ErrUnknownPreset
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		if on, err := s.GloballyPaused(ctx); err != nil {
			return err
		} else if on {
			return ErrGloballyPaused
		}
		row, err := s.loadRow(ctx, id)
		if err != nil {
			return err
		}
		set, err := decodeSettings(row, p)
		if err != nil {
			return err
		}
		set.Reason = ""
		task, ts, err := s.lookupTask(ctx, p)
		if err != nil {
			return err
		}
		if ts == taskForeign {
			return fmt.Errorf("%w: %s belongs to folder %q", ErrTaskConflict, task.ID, task.ChatFolder)
		}
		env, err := s.env(ctx)
		if err != nil {
			return err
		}
		now := s.now().UnixMilli()

		if ts == taskLive && unmanaged(row) {
			return fmt.Errorf("%w: %s exists but gopod did not create it; reset it to the template first", ErrUserModified, task.ID)
		}
		if ts == taskLive && modified(row, task) {
			// Owner-edited: reactivate the owner's definition as is. By design this
			// skips the quiet-hours check: the owner's edit is the owner's decision
			// (no restrictions for the owner, 2026-10-30).
			next, err := s.schedulerNext(task.ScheduleType, task.ScheduleValue)
			if err != nil {
				return err
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "active", next, task.LastRun); err != nil {
				return err
			}
			return s.putRow(ctx, row, set, row.TemplateVersion, row.AppliedHash)
		}

		prompt, sched, err := s.renderTask(p, set.Settings, env)
		if err != nil {
			return fmt.Errorf("enabling %s: %w", id, err)
		}
		switch ts {
		case taskLive:
			next := task.NextRun
			if task.Status != "active" || next <= 0 || sched != task.ScheduleValue {
				if next, err = s.schedulerNext("cron", sched); err != nil {
					return err
				}
			}
			task.Prompt, task.ScheduleType, task.ScheduleValue, task.NextRun = prompt, "cron", sched, next
			if err = s.st.UpdateTaskDefinition(ctx, task); err != nil {
				return err
			}
			if err = s.st.UpdateTaskStatus(ctx, task.ID, "active", next, task.LastRun); err != nil {
				return err
			}
		default:
			if ts == taskDone { // IPC cancel leaves a finished row that blocks the deterministic id
				if err = s.st.DeleteTask(ctx, task.ID); err != nil {
					return err
				}
			}
			next, err := s.schedulerNext("cron", sched)
			if err != nil {
				return err
			}
			if _, err = s.st.CreateTask(ctx, store.TaskRecord{
				ID: TaskID(id), ChatFolder: s.folder, ChatJID: s.jid, Prompt: prompt,
				ScheduleType: "cron", ScheduleValue: sched, NextRun: next, Status: "active", CreatedAt: now,
			}); err != nil {
				return err
			}
		}
		return s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched))
	})
}

func (s *Service) putRow(ctx context.Context, row store.ProactivePreset, set storedSettings, version int, hash string) error {
	row.TaskID = TaskID(row.PresetID)
	row.Settings = encodeSettings(set)
	row.TemplateVersion, row.AppliedHash = version, hash
	row.UpdatedAt = s.now().UnixMilli()
	return s.st.PutProactivePreset(ctx, row)
}

// Disable pauses the preset's task; the task and its history stay. It also
// takes the preset out of the set a global pause would restore. No task: no-op.
func (s *Service) Disable(ctx context.Context, id string) error {
	p, ok := Lookup(id)
	if !ok {
		return ErrUnknownPreset
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		task, ts, err := s.lookupTask(ctx, p)
		if err != nil {
			return err
		}
		if ts != taskLive {
			return nil
		}
		row, err := s.loadRow(ctx, id)
		if err != nil {
			return err
		}
		if unmanaged(row) {
			return fmt.Errorf("%w: %s exists but gopod did not create it; reset it to the template first", ErrUserModified, task.ID)
		}
		if err = s.st.UpdateTaskStatus(ctx, task.ID, "paused", 0, task.LastRun); err != nil {
			return err
		}
		set, err := decodeSettings(row, p)
		if err != nil {
			return err
		}
		set.Reason = ""
		row.TaskID, row.Settings, row.UpdatedAt = task.ID, encodeSettings(set), s.now().UnixMilli()
		if err = s.st.PutProactivePreset(ctx, row); err != nil {
			return err
		}
		return s.dropFromGlobalSet(ctx, id)
	})
}

// Configure validates and stores new settings. For a live, unedited task the
// prompt and schedule are rewritten; an edited task is refused with
// ErrUserModified. A gopod write never counts as an owner edit.
func (s *Service) Configure(ctx context.Context, id string, next Settings) error {
	return s.UpdateSettings(ctx, id, func(cur *Settings) error { *cur = next; return nil })
}

// UpdateSettings is Configure as a read-modify-write: fn receives the stored
// settings and changes them, all inside the task lock and one transaction, so
// two quick changes of different fields cannot overwrite each other. An error
// from fn, or settings the preset does not offer, changes nothing.
func (s *Service) UpdateSettings(ctx context.Context, id string, fn func(*Settings) error) error {
	p, ok := Lookup(id)
	if !ok {
		return ErrUnknownPreset
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		row, err := s.loadRow(ctx, id)
		if err != nil {
			return err
		}
		cur, err := decodeSettings(row, p)
		if err != nil {
			return err
		}
		next := cur.Settings
		if err = fn(&next); err != nil {
			return err
		}
		if err = p.ValidateSettings(next); err != nil {
			return err
		}
		task, ts, err := s.lookupTask(ctx, p)
		if err != nil {
			return err
		}
		if ts == taskLive && (unmanaged(row) || modified(row, task)) {
			return ErrUserModified
		}
		env, err := s.env(ctx)
		if err != nil {
			return err
		}
		prompt, sched, err := s.renderTask(p, next, env)
		if err != nil {
			return fmt.Errorf("configuring %s: %w", id, err)
		}
		set := storedSettings{Settings: next}
		if ts != taskLive {
			row.PresetID = id
			row.Settings, row.UpdatedAt = encodeSettings(set), s.now().UnixMilli()
			return s.st.PutProactivePreset(ctx, row)
		}
		if err = s.rewrite(ctx, &task, prompt, sched); err != nil {
			return err
		}
		return s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched))
	})
}

// rewrite replaces a live task's definition, keeping its status. An active task
// gets a fresh next run; a paused one keeps its next_run (0, or the global pause
// marker, which must survive a rewrite).
func (s *Service) rewrite(ctx context.Context, task *store.TaskRecord, prompt, sched string) error {
	next := task.NextRun
	if task.Status == "active" {
		var err error
		if next, err = s.schedulerNext("cron", sched); err != nil {
			return err
		}
	}
	task.Prompt, task.ScheduleType, task.ScheduleValue, task.NextRun = prompt, "cron", sched, next
	return s.st.UpdateTaskDefinition(ctx, *task)
}

// ResetToTemplate rewrites a live task from the current template and stored
// settings, discarding owner edits and applying a newer template version.
func (s *Service) ResetToTemplate(ctx context.Context, id string) error {
	p, ok := Lookup(id)
	if !ok {
		return ErrUnknownPreset
	}
	return s.inLockedTx(ctx, func(ctx context.Context) error {
		row, err := s.loadRow(ctx, id)
		if err != nil {
			return err
		}
		task, ts, err := s.lookupTask(ctx, p)
		if err != nil {
			return err
		}
		if ts != taskLive {
			return ErrNotEnabled
		}
		set, err := decodeSettings(row, p)
		if err != nil {
			return err
		}
		env, err := s.env(ctx)
		if err != nil {
			return err
		}
		prompt, sched, err := s.renderTask(p, set.Settings, env)
		if err != nil {
			return fmt.Errorf("resetting %s: %w", id, err)
		}
		if err = s.rewrite(ctx, &task, prompt, sched); err != nil {
			return err
		}
		return s.putRow(ctx, row, set, p.TemplateVersion, ContentHash(prompt, "cron", sched))
	})
}
