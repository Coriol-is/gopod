package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

var (
	// ErrUnknownPreset is returned for an id that is not in the catalog.
	ErrUnknownPreset = errors.New("proactive: unknown preset")
	// ErrGloballyPaused is returned by Enable while the global pause is on.
	ErrGloballyPaused = errors.New("proactive: all presets are paused (global pause)")
	// ErrUserModified means the task was edited outside gopod (/tasks or IPC);
	// gopod will not overwrite it except through ResetToTemplate.
	ErrUserModified = errors.New("proactive: the task was edited outside /proactive")
	// ErrNotEnabled is returned when an operation needs an existing preset task.
	ErrNotEnabled = errors.New("proactive: preset has no task")
	// ErrTaskConflict means a task already holds the deterministic preset id but
	// belongs to another folder or chat. It is never adopted or modified.
	ErrTaskConflict = errors.New("proactive: a foreign task holds this preset's id")
)

// ReasonInsideQuiet is stored when quiet hours paused a preset.
const ReasonInsideQuiet = "inside quiet hours"

// ReasonNoRunHours is stored when quiet hours leave no hour of the working window.
const ReasonNoRunHours = "no run time in the working window"

// pauseReason names why a render failed with blocked(err).
func pauseReason(err error) string {
	if errors.Is(err, ErrNoRunHours) {
		return ReasonNoRunHours
	}
	return ReasonInsideQuiet
}

// Setting keys in proactive_settings.
const (
	keyTZ        = "tz"
	keyWindow    = "window"     // "09-19"
	keyQuiet     = "quiet"      // "2200-0800"
	keyGlobalOff = "global_off" // JSON list of preset ids paused by the global pause
)

// Config wires a Service. Nothing in production constructs one yet.
type Config struct {
	Store   *store.Store
	Folder  string // owner chat folder
	ChatJID string // tg:<owner chat id>
	Now     func() time.Time
}

// Service enables, disables and reschedules presets as ordinary scheduled
// tasks. Every mutating method takes the task lock (scheduler.WithTaskLock, safe
// under the IPC path that already holds it), so it serializes with the
// scheduler's snapshot, enqueue and advance, and runs in one store transaction.
type Service struct {
	st     *store.Store
	folder string
	jid    string
	now    func() time.Time
}

// New validates cfg and returns a Service.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("proactive: nil store")
	}
	if cfg.Folder == "" || cfg.ChatJID == "" {
		return nil, errors.New("proactive: owner folder and chat jid are required")
	}
	now := cfg.Now
	if now == nil {
		now = time.Now
	}
	return &Service{st: cfg.Store, folder: cfg.Folder, jid: cfg.ChatJID, now: now}, nil
}

// storedSettings is the JSON in proactive_presets.settings.
type storedSettings struct {
	Settings
	Reason string `json:"reason,omitempty"` // why gopod paused the task
}

func decodeSettings(row store.ProactivePreset, p Preset) (storedSettings, error) {
	var s storedSettings
	if row.Settings != "" {
		if err := json.Unmarshal([]byte(row.Settings), &s); err != nil {
			return storedSettings{}, fmt.Errorf("proactive: corrupt settings of %s: %w", p.ID, err)
		}
	}
	if s.Freq == "" {
		reason := s.Reason
		s = storedSettings{Settings: p.DefaultSettings(), Reason: reason}
	}
	return s, nil
}

func encodeSettings(s storedSettings) string {
	b, _ := json.Marshal(s) // plain struct, cannot fail
	return string(b)
}

// taskState classifies the scheduled task behind a preset.
type taskState int

const (
	taskNone    taskState = iota // never created or deleted
	taskDone                     // finished or cancelled by IPC; row still present
	taskLive                     // active or paused, ours
	taskForeign                  // holds the id but belongs to another folder or chat
)

func (s *Service) lookupTask(ctx context.Context, p Preset) (store.TaskRecord, taskState, error) {
	t, err := s.st.GetTask(ctx, TaskID(p.ID))
	if errors.Is(err, store.ErrTaskNotFound) {
		return store.TaskRecord{}, taskNone, nil
	}
	if err != nil {
		return store.TaskRecord{}, taskNone, fmt.Errorf("proactive: loading task for %s: %w", p.ID, err)
	}
	if t.ChatFolder != s.folder || t.ChatJID != s.jid {
		return t, taskForeign, nil
	}
	if t.Status == "done" {
		return t, taskDone, nil
	}
	return t, taskLive, nil
}

// modified reports whether the live task differs from what gopod last wrote.
func modified(row store.ProactivePreset, t store.TaskRecord) bool {
	return row.AppliedHash != ContentHash(t.Prompt, t.ScheduleType, t.ScheduleValue)
}

// unmanaged reports a live task under a preset id that gopod never wrote (no
// applied hash): it was planted, for example by an owner-folder agent over IPC,
// and must not be adopted, reconfigured or resumed. ResetToTemplate is the
// owner's explicit way to take it over.
func unmanaged(row store.ProactivePreset) bool { return row.AppliedHash == "" }

func (s *Service) loadRow(ctx context.Context, id string) (store.ProactivePreset, error) {
	row, ok, err := s.st.GetProactivePreset(ctx, id)
	if err != nil {
		return store.ProactivePreset{}, err
	}
	if !ok {
		row = store.ProactivePreset{PresetID: id}
	}
	return row, nil
}

// --- settings (timezone, working window, quiet hours, global pause) ---
// An unset key means the default; a stored value that does not parse is an
// error, never a silent fallback.

func (s *Service) setting(ctx context.Context, key string) (string, error) {
	v, _, err := s.st.GetProactiveSetting(ctx, key)
	return v, err
}

// Timezone returns the owner zone (DefaultTimezone when unset).
func (s *Service) Timezone(ctx context.Context) (string, error) {
	v, err := s.setting(ctx, keyTZ)
	if err != nil || v == "" {
		if err != nil {
			return "", err
		}
		return DefaultTimezone, nil
	}
	if err = ValidateTimezone(v); err != nil {
		return "", fmt.Errorf("proactive: stored timezone: %w", err)
	}
	return v, nil
}

// Window returns the working window (DefaultWindow when unset).
func (s *Service) Window(ctx context.Context) (Window, error) {
	v, err := s.setting(ctx, keyWindow)
	if err != nil || v == "" {
		return DefaultWindow, err
	}
	from, to, ok := strings.Cut(v, "-")
	a, err1 := strconv.Atoi(from)
	b, err2 := strconv.Atoi(to)
	w := Window{StartHour: a, EndHour: b}
	if !ok || err1 != nil || err2 != nil || w.validate() != nil {
		return Window{}, fmt.Errorf("proactive: stored working window %q is invalid", v)
	}
	return w, nil
}

// Quiet returns the quiet window, or nil when quiet hours are off.
func (s *Service) Quiet(ctx context.Context) (*Quiet, error) {
	v, err := s.setting(ctx, keyQuiet)
	if err != nil || v == "" {
		return nil, err
	}
	from, to, ok := strings.Cut(v, "-")
	if !ok {
		return nil, fmt.Errorf("proactive: stored quiet window %q is invalid", v)
	}
	q, err := ParseQuiet(from, to)
	if err != nil {
		return nil, fmt.Errorf("proactive: stored quiet window: %w", err)
	}
	return &q, nil
}

func (s *Service) env(ctx context.Context) (Env, error) {
	tz, err := s.Timezone(ctx)
	if err != nil {
		return Env{}, err
	}
	w, err := s.Window(ctx)
	if err != nil {
		return Env{}, err
	}
	q, err := s.Quiet(ctx)
	if err != nil {
		return Env{}, err
	}
	return Env{TZ: tz, Window: w, Quiet: q}, nil
}

func (s *Service) globalOff(ctx context.Context) (ids []string, on bool, err error) {
	v, ok, err := s.st.GetProactiveSetting(ctx, keyGlobalOff)
	if err != nil || !ok {
		return nil, false, err
	}
	if err = json.Unmarshal([]byte(v), &ids); err != nil {
		return nil, true, fmt.Errorf("proactive: corrupt global pause set: %w", err)
	}
	return ids, true, nil
}

// GloballyPaused reports whether the global pause is on.
func (s *Service) GloballyPaused(ctx context.Context) (bool, error) {
	_, on, err := s.globalOff(ctx)
	return on, err
}

// schedulerNext computes the next run for a task definition from now.
func (s *Service) schedulerNext(scheduleType, value string) (int64, error) {
	next, err := scheduler.FirstRunAt(scheduleType, value, s.now())
	if err != nil {
		return 0, fmt.Errorf("proactive: computing next run of %q: %w", value, err)
	}
	return next, nil
}

// renderTask renders prompt and schedule for a preset under env.
func (s *Service) renderTask(p Preset, set Settings, env Env) (prompt, schedule string, err error) {
	schedule, err = Render(p, set, env)
	if err != nil {
		return "", "", err
	}
	return p.Prompt(PromptInput{TZ: env.TZ, OwnerJID: s.jid, Settings: set}), schedule, nil
}

// blocked reports a render failure that means "no valid run time under the
// current quiet hours" rather than a real error.
func blocked(err error) bool { return errors.Is(err, ErrInsideQuiet) || errors.Is(err, ErrNoRunHours) }
