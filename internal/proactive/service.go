package proactive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
)

// ReasonInsideQuiet is stored when quiet hours paused a preset.
const ReasonInsideQuiet = "inside quiet hours"

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
// tasks. Every mutating method takes scheduler.LockTasks, so it serializes
// with the scheduler's snapshot, enqueue and advance, and runs in one store
// transaction.
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

func decodeSettings(row store.ProactivePreset, p Preset) storedSettings {
	var s storedSettings
	if row.Settings != "" {
		_ = json.Unmarshal([]byte(row.Settings), &s)
	}
	if s.Freq == "" {
		reason := s.Reason
		s = storedSettings{Settings: p.DefaultSettings(), Reason: reason}
	}
	return s
}

func encodeSettings(s storedSettings) string {
	b, _ := json.Marshal(s) // plain struct, cannot fail
	return string(b)
}

// taskState classifies the scheduled task behind a preset.
type taskState int

const (
	taskNone taskState = iota // never created or deleted
	taskDone                  // finished or cancelled by IPC; row still present
	taskLive                  // active or paused
)

func (s *Service) lookupTask(ctx context.Context, p Preset) (store.TaskRecord, taskState, error) {
	t, err := s.st.GetTask(ctx, TaskID(p.ID))
	if errors.Is(err, store.ErrTaskNotFound) {
		return store.TaskRecord{}, taskNone, nil
	}
	if err != nil {
		return store.TaskRecord{}, taskNone, fmt.Errorf("proactive: loading task for %s: %w", p.ID, err)
	}
	if t.Status == "done" {
		return t, taskDone, nil
	}
	return t, taskLive, nil
}

// modified reports whether the live task differs from what gopod last wrote.
func modified(row store.ProactivePreset, t store.TaskRecord) bool {
	return row.AppliedHash != ContentHash(t.Prompt, t.ScheduleValue)
}

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

func (s *Service) setting(ctx context.Context, key string) string {
	v, _, err := s.st.GetProactiveSetting(ctx, key)
	if err != nil {
		return ""
	}
	return v
}

// Timezone returns the owner zone (DefaultTimezone when unset).
func (s *Service) Timezone(ctx context.Context) string {
	if v := s.setting(ctx, keyTZ); v != "" && ValidateTimezone(v) == nil {
		return v
	}
	return DefaultTimezone
}

// Window returns the working window (DefaultWindow when unset).
func (s *Service) Window(ctx context.Context) Window {
	var w Window
	if _, err := fmt.Sscanf(s.setting(ctx, keyWindow), "%d-%d", &w.StartHour, &w.EndHour); err != nil || w.validate() != nil {
		return DefaultWindow
	}
	return w
}

// Quiet returns the quiet window, or nil when quiet hours are off.
func (s *Service) Quiet(ctx context.Context) *Quiet {
	from, to, ok := strings.Cut(s.setting(ctx, keyQuiet), "-")
	if !ok {
		return nil
	}
	q, err := ParseQuiet(from, to)
	if err != nil {
		return nil
	}
	return &q
}

func (s *Service) env(ctx context.Context) Env {
	return Env{TZ: s.Timezone(ctx), Window: s.Window(ctx), Quiet: s.Quiet(ctx)}
}

func (s *Service) globalOff(ctx context.Context) ([]string, bool) {
	v, ok, err := s.st.GetProactiveSetting(ctx, keyGlobalOff)
	if err != nil || !ok {
		return nil, false
	}
	var ids []string
	_ = json.Unmarshal([]byte(v), &ids)
	return ids, true
}

// GloballyPaused reports whether the global pause is on.
func (s *Service) GloballyPaused(ctx context.Context) bool {
	_, on := s.globalOff(ctx)
	return on
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
