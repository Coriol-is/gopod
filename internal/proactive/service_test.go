package proactive

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

type fixture struct {
	t   *testing.T
	st  *store.Store
	svc *Service
	now time.Time
	mu  sync.Mutex
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	f := &fixture{t: t, st: st, now: time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)} // a Friday
	f.svc = f.service()
	return f
}

func (f *fixture) service() *Service {
	svc, err := New(Config{Store: f.st, Folder: "owner", ChatJID: "tg:1", Now: f.clock})
	if err != nil {
		f.t.Fatal(err)
	}
	return svc
}

func (f *fixture) enable(id string) {
	f.t.Helper()
	if err := f.svc.Enable(context.Background(), id); err != nil {
		f.t.Fatalf("Enable(%s): %v", id, err)
	}
}

func (f *fixture) task(id string) store.TaskRecord {
	f.t.Helper()
	tk, err := f.st.GetTask(context.Background(), TaskID(id))
	if err != nil {
		f.t.Fatalf("GetTask(%s): %v", id, err)
	}
	return tk
}

func (f *fixture) status(id string) Status {
	f.t.Helper()
	s, err := f.svc.Status(context.Background(), id)
	if err != nil {
		f.t.Fatal(err)
	}
	return s
}

func (f *fixture) editPrompt(id, prompt string) {
	f.t.Helper()
	tk := f.task(id)
	tk.Prompt = prompt
	if err := f.st.UpdateTaskDefinition(context.Background(), tk); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) taskCount() int {
	f.t.Helper()
	list, err := f.st.ListTasksByChat(context.Background(), "owner")
	if err != nil {
		f.t.Fatal(err)
	}
	return len(list)
}

func TestNewRequiresOwnerTarget(t *testing.T) {
	if _, err := New(Config{Store: nil, Folder: "owner", ChatJID: "tg:1"}); err == nil {
		t.Error("nil store accepted")
	}
	f := newFixture(t)
	if _, err := New(Config{Store: f.st, Folder: "", ChatJID: "tg:1"}); err == nil {
		t.Error("empty folder accepted")
	}
	if _, err := New(Config{Store: f.st, Folder: "owner", ChatJID: ""}); err == nil {
		t.Error("empty jid accepted")
	}
}

func TestAllPresetsStartOff(t *testing.T) {
	f := newFixture(t)
	list, err := f.svc.List(context.Background())
	if err != nil || len(list) != 6 {
		t.Fatalf("list: %d %v", len(list), err)
	}
	for _, s := range list {
		if s.State != StateOff {
			t.Errorf("%s starts %s", s.Preset.ID, s.State)
		}
	}
	if f.taskCount() != 0 {
		t.Fatal("tasks exist before any enable")
	}
}

func TestEnableCreatesOneOrdinaryTaskAndIsIdempotent(t *testing.T) {
	f := newFixture(t)
	f.enable("gmail.morning")
	f.enable("gmail.morning")
	if f.taskCount() != 1 {
		t.Fatalf("tasks = %d, want 1", f.taskCount())
	}
	tk := f.task("gmail.morning")
	wantNext, _ := scheduler.FirstRunAt("cron", "CRON_TZ=Europe/Belgrade 0 8 * * *", f.now)
	if tk.ID != "preset-gmail.morning" || tk.ChatFolder != "owner" || tk.ChatJID != "tg:1" || tk.ScheduleType != "cron" ||
		tk.ScheduleValue != "CRON_TZ=Europe/Belgrade 0 8 * * *" || tk.Status != "active" || tk.NextRun != wantNext {
		t.Fatalf("task = %+v (want next %d)", tk, wantNext)
	}
	if !strings.Contains(tk.Prompt, "tg:1") || strings.Contains(tk.Prompt, "{{") {
		t.Fatalf("prompt not rendered: %s", tk.Prompt)
	}
	st := f.status("gmail.morning")
	if st.State != StateActive || st.Modified || st.TemplateOutdated || st.TaskID != tk.ID {
		t.Fatalf("status = %+v", st)
	}
}

func TestEnableRejectsUnknownPresetAndInsideQuiet(t *testing.T) {
	f := newFixture(t)
	if err := f.svc.Enable(context.Background(), "gmail.nope"); !errors.Is(err, ErrUnknownPreset) {
		t.Fatalf("unknown: %v", err)
	}
	q, _ := ParseQuiet("0600", "0900")
	if err := f.svc.SetQuiet(context.Background(), &q); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Enable(context.Background(), "gmail.morning"); !errors.Is(err, ErrInsideQuiet) {
		t.Fatalf("inside quiet: %v", err)
	}
	if f.taskCount() != 0 {
		t.Fatal("a refused enable left a task")
	}
}

func TestConcurrentEnableCreatesOneTask(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := f.svc.Enable(context.Background(), "cal.agenda"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if f.taskCount() != 1 {
		t.Fatalf("tasks = %d", f.taskCount())
	}
}

func TestDisableKeepsTaskAndHistoryAndEnableReusesIt(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.evening")
	tk := f.task("gmail.evening")
	if err := f.st.UpdateTaskStatus(ctx, tk.ID, "active", tk.NextRun, 12345); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.Disable(ctx, "gmail.evening"); err != nil {
		t.Fatal(err)
	}
	off := f.task("gmail.evening")
	if off.Status != "paused" || off.NextRun != 0 || off.LastRun != 12345 || f.status("gmail.evening").State != StatePaused {
		t.Fatalf("after disable: %+v", off)
	}
	f.enable("gmail.evening")
	on := f.task("gmail.evening")
	if on.Status != "active" || on.NextRun == 0 || on.LastRun != 12345 || on.CreatedAt != tk.CreatedAt || f.taskCount() != 1 {
		t.Fatalf("after re-enable: %+v", on)
	}
	if err := f.svc.Disable(ctx, "gmail.morning"); err != nil { // never enabled: no-op
		t.Fatalf("disable of a never-enabled preset: %v", err)
	}
}

func TestCancelledOrFinishedTaskReadsOffAndEnableCreatesFresh(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("cal.agenda")
	if err := f.st.DeleteTask(ctx, TaskID("cal.agenda")); err != nil { // /tasks cancel
		t.Fatal(err)
	}
	if s := f.status("cal.agenda"); s.State != StateOff {
		t.Fatalf("deleted task reads %s", s.State)
	}
	f.enable("cal.agenda")
	if f.task("cal.agenda").Status != "active" {
		t.Fatal("fresh task not active")
	}

	// IPC cancel leaves a done row: the deterministic id must still be reusable.
	if err := f.st.UpdateTaskStatus(ctx, TaskID("cal.agenda"), "done", 0, 0); err != nil {
		t.Fatal(err)
	}
	if s := f.status("cal.agenda"); s.State != StateOff {
		t.Fatalf("done task reads %s", s.State)
	}
	f.enable("cal.agenda")
	if tk := f.task("cal.agenda"); tk.Status != "active" || tk.NextRun == 0 || f.taskCount() != 1 {
		t.Fatalf("after done: %+v", tk)
	}
}

func TestOwnerEditIsDetectedAndPreserved(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.urgent")
	f.editPrompt("gmail.urgent", "my own prompt")
	if s := f.status("gmail.urgent"); !s.Modified {
		t.Fatal("edit not detected")
	}
	if err := f.svc.Configure(ctx, "gmail.urgent", Settings{Freq: "30m"}); !errors.Is(err, ErrUserModified) {
		t.Fatalf("Configure over an edit: %v", err)
	}
	if err := f.svc.Disable(ctx, "gmail.urgent"); err != nil {
		t.Fatal(err)
	}
	f.enable("gmail.urgent") // reactivates, must not rewrite
	tk := f.task("gmail.urgent")
	if tk.Prompt != "my own prompt" || tk.Status != "active" || tk.NextRun == 0 {
		t.Fatalf("edit lost or task not active: %+v", tk)
	}
	if err := f.svc.ResetToTemplate(ctx, "gmail.urgent"); err != nil {
		t.Fatal(err)
	}
	if s := f.status("gmail.urgent"); s.Modified || strings.Contains(f.task("gmail.urgent").Prompt, "my own") {
		t.Fatalf("reset failed: %+v", s)
	}
}

func TestConfigureRewritesScheduleAndRehashes(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.urgent")
	if err := f.svc.Configure(ctx, "gmail.urgent", Settings{Freq: "15m"}); err != nil {
		t.Fatal(err)
	}
	if tk := f.task("gmail.urgent"); tk.ScheduleValue != "CRON_TZ=Europe/Belgrade */15 9-18 * * 1-5" {
		t.Fatalf("schedule = %s", tk.ScheduleValue)
	}
	if s := f.status("gmail.urgent"); s.Modified || s.Settings.Freq != "15m" {
		t.Fatalf("a gopod write must not count as an owner edit: %+v", s)
	}
	if err := f.svc.Configure(ctx, "gmail.urgent", Settings{Freq: "7m"}); err == nil {
		t.Fatal("invalid settings accepted")
	}
	// Settings of a preset that is off are stored and used by the next enable.
	if err := f.svc.Configure(ctx, "gmail.morning", Settings{Freq: "weekdays", Time: "0730"}); err != nil {
		t.Fatal(err)
	}
	if f.taskCount() != 1 {
		t.Fatal("Configure on an off preset created a task")
	}
	f.enable("gmail.morning")
	if got := f.task("gmail.morning").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 30 7 * * 1-5" {
		t.Fatalf("schedule = %s", got)
	}
}

func TestCalReminderLeadIsWrittenIntoThePollPrompt(t *testing.T) {
	f := newFixture(t)
	f.enable("cal.reminder")
	if !strings.Contains(f.task("cal.reminder").Prompt, "10 minutes") {
		t.Fatal("default lead missing")
	}
	if err := f.svc.Configure(context.Background(), "cal.reminder", Settings{Freq: "30m", Lead: 60}); err != nil {
		t.Fatal(err)
	}
	if p := f.task("cal.reminder").Prompt; !strings.Contains(p, "90 minutes") {
		t.Fatalf("lookahead not rewritten: %s", p)
	}
}

func bumpTemplate(t *testing.T, id, suffix string) {
	t.Helper()
	for i := range catalog {
		if catalog[i].ID == id {
			old := catalog[i]
			catalog[i].TemplateVersion++
			catalog[i].body += suffix
			t.Cleanup(func() { catalog[i] = old })
			return
		}
	}
	t.Fatal("no such preset")
}

func TestTemplateUpgradeRewritesOnlyUntouchedTasks(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.evening")
	f.enable("gmail.urgent")
	f.enable("cal.weekly")
	f.editPrompt("gmail.urgent", "owner version")
	bumpTemplate(t, "gmail.evening", " NEW TEXT")
	bumpTemplate(t, "gmail.urgent", " NEW TEXT")

	res, err := f.svc.UpgradeTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]UpgradeOutcome{}
	for _, r := range res {
		got[r.PresetID] = r.Outcome
	}
	if len(got) != 2 || got["gmail.evening"] != UpgradeApplied || got["gmail.urgent"] != UpgradeNeedsApply {
		t.Fatalf("results = %v", got)
	}
	if !strings.Contains(f.task("gmail.evening").Prompt, "NEW TEXT") {
		t.Fatal("untouched task not upgraded")
	}
	if s := f.status("gmail.evening"); s.TemplateOutdated || s.Modified {
		t.Fatalf("evening status %+v", s)
	}
	if f.task("gmail.urgent").Prompt != "owner version" {
		t.Fatal("owner edit overwritten")
	}
	if s := f.status("gmail.urgent"); !s.TemplateOutdated || !s.Modified {
		t.Fatalf("urgent status %+v", s)
	}
	if again, _ := f.svc.UpgradeTemplates(ctx); len(again) != 1 || again[0].Outcome != UpgradeNeedsApply {
		t.Fatalf("second pass: %+v", again)
	}
	if err := f.svc.ResetToTemplate(ctx, "gmail.urgent"); err != nil {
		t.Fatal(err)
	}
	if s := f.status("gmail.urgent"); s.TemplateOutdated || s.Modified || !strings.Contains(f.task("gmail.urgent").Prompt, "NEW TEXT") {
		t.Fatalf("apply failed: %+v", s)
	}
}

func TestGlobalPauseRestoresExactlyTheActiveSet(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	f.enable("cal.agenda")
	f.enable("cal.weekly")
	if err := f.svc.Disable(ctx, "cal.weekly"); err != nil { // off before the pause
		t.Fatal(err)
	}
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.PauseAll(ctx); err != nil { // idempotent, must not overwrite the saved set
		t.Fatal(err)
	}
	for _, id := range []string{"gmail.morning", "cal.agenda", "cal.weekly"} {
		if tk := f.task(id); tk.Status != "paused" || tk.NextRun != 0 {
			t.Fatalf("%s after pause: %+v", id, tk)
		}
	}
	if !f.svc.GloballyPaused(ctx) {
		t.Fatal("not reported paused")
	}
	if err := f.svc.Enable(ctx, "gmail.evening"); !errors.Is(err, ErrGloballyPaused) {
		t.Fatalf("enable during pause: %v", err)
	}
	if err := f.svc.Disable(ctx, "cal.agenda"); err != nil { // owner turns one off during the pause
		t.Fatal(err)
	}
	if err := f.svc.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if f.svc.GloballyPaused(ctx) {
		t.Fatal("still paused")
	}
	if tk := f.task("gmail.morning"); tk.Status != "active" || tk.NextRun == 0 {
		t.Fatalf("morning not restored: %+v", tk)
	}
	for _, id := range []string{"cal.agenda", "cal.weekly"} {
		if f.task(id).Status != "paused" {
			t.Fatalf("%s must stay off", id)
		}
	}
	if err := f.svc.ResumeAll(ctx); err != nil { // nothing saved: no-op
		t.Fatal(err)
	}
}

func TestOwnerMayResumeAPresetFromTasksDuringPause(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	tk := f.task("gmail.morning")
	if err := f.st.UpdateTaskStatus(ctx, tk.ID, "active", time.Now().UnixMilli(), 0); err != nil {
		t.Fatal(err)
	}
	if s := f.status("gmail.morning"); s.State != StateActive || !s.GloballyPaused {
		t.Fatalf("status must show the real task state under a pause: %+v", s)
	}
	if err := f.svc.ResumeAll(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestTimezoneDefaultAndValidation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if tz := f.svc.Timezone(ctx); tz != "Europe/Belgrade" {
		t.Fatalf("default = %s", tz)
	}
	f.enable("gmail.morning")
	before := f.task("gmail.morning")
	for _, bad := range []string{"", "Mars/Base", "Europe/Belgrad", "Local", "Europe/Belgrade extra", strings.Repeat("a", 65)} {
		if _, err := f.svc.SetTimezone(ctx, bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if f.task("gmail.morning") != before || f.svc.Timezone(ctx) != "Europe/Belgrade" {
		t.Fatal("a rejected zone changed state")
	}
	for _, ok := range []string{"UTC", "Asia/Tbilisi"} {
		if _, err := f.svc.SetTimezone(ctx, ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
}

func TestSetTimezoneRerendersEveryPresetAndPersists(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	f.enable("cal.agenda")
	f.enable("gmail.urgent")
	f.enable("cal.weekly")
	if err := f.svc.Disable(ctx, "cal.weekly"); err != nil { // paused tasks move zone too, stay paused
		t.Fatal(err)
	}
	f.editPrompt("cal.agenda", "my agenda prompt mentioning Europe/Belgrade")
	oldNext := f.task("gmail.morning").NextRun

	rep, err := f.svc.SetTimezone(ctx, "America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Kept) != 1 || rep.Kept[0] != "cal.agenda" || len(rep.Rescheduled) != 3 {
		t.Fatalf("report = %+v", rep)
	}
	for _, id := range []string{"gmail.morning", "cal.agenda", "gmail.urgent", "cal.weekly"} {
		if tk := f.task(id); !strings.HasPrefix(tk.ScheduleValue, "CRON_TZ=America/New_York ") {
			t.Errorf("%s schedule = %s", id, tk.ScheduleValue)
		}
	}
	m := f.task("gmail.morning")
	wantNext, _ := scheduler.FirstRunAt("cron", "CRON_TZ=America/New_York 0 8 * * *", f.now)
	if m.NextRun != wantNext || m.NextRun == oldNext || !strings.Contains(m.Prompt, "America/New_York") {
		t.Fatalf("morning not recomputed: %+v (old %d want %d)", m, oldNext, wantNext)
	}
	if ag := f.task("cal.agenda"); ag.Prompt != "my agenda prompt mentioning Europe/Belgrade" || ag.NextRun == 0 {
		t.Fatalf("edited preset: %+v", ag)
	}
	if wk := f.task("cal.weekly"); wk.Status != "paused" || wk.NextRun != 0 {
		t.Fatalf("paused preset: %+v", wk)
	}
	if s := f.status("gmail.morning"); s.Modified {
		t.Fatal("zone change counted as an owner edit")
	}
	if s := f.status("cal.agenda"); !s.Modified {
		t.Fatal("edited preset lost its modified mark")
	}
	if f.service().Timezone(ctx) != "America/New_York" { // survives a restart
		t.Fatal("zone not persisted")
	}
}

func TestQuietHoursPauseFixedPresetsAndTrimPollers(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning") // 08:00
	f.enable("cal.agenda")    // 08:00
	f.enable("gmail.urgent")  // hourly 9-18
	q, _ := ParseQuiet("1200", "1400")
	if err := f.svc.SetQuiet(ctx, &q); err != nil {
		t.Fatal(err)
	}
	if got := f.task("gmail.urgent").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 0 9-11,14-18 * * 1-5" {
		t.Fatalf("urgent = %s", got)
	}
	if f.task("gmail.morning").Status != "active" {
		t.Fatal("08:00 is outside 12-14 and must keep running")
	}
	q2, _ := ParseQuiet("0700", "0900")
	if err := f.svc.SetQuiet(ctx, &q2); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gmail.morning", "cal.agenda"} {
		tk := f.task(id)
		if tk.Status != "paused" || tk.NextRun != 0 {
			t.Fatalf("%s not paused: %+v", id, tk)
		}
		if s := f.status(id); s.PausedReason != ReasonInsideQuiet || s.State != StatePaused {
			t.Fatalf("%s status %+v", id, s)
		}
	}
	if f.task("gmail.urgent").Status != "active" {
		t.Fatal("poller lost")
	}
	if err := f.svc.SetQuiet(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if got := f.task("gmail.urgent").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 0 9-18 * * 1-5" {
		t.Fatalf("urgent after clearing = %s", got)
	}
	if f.task("gmail.morning").Status != "paused" {
		t.Fatal("nothing is shifted or resumed silently")
	}
	f.enable("gmail.morning") // owner re-enables: reason cleared
	if s := f.status("gmail.morning"); s.PausedReason != "" || s.State != StateActive {
		t.Fatalf("after enable %+v", s)
	}
}

func TestQuietCoveringTheWindowPausesPollers(t *testing.T) {
	f := newFixture(t)
	f.enable("gmail.urgent")
	q, _ := ParseQuiet("0800", "2000")
	if err := f.svc.SetQuiet(context.Background(), &q); err != nil {
		t.Fatal(err)
	}
	if s := f.status("gmail.urgent"); s.State != StatePaused || s.PausedReason != ReasonInsideQuiet {
		t.Fatalf("status %+v", s)
	}
}

func TestSetWindowRerendersPollersOnly(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.urgent")
	f.enable("gmail.evening")
	evening := f.task("gmail.evening")
	if err := f.svc.SetWindow(ctx, Window{StartHour: 8, EndHour: 20}); err != nil {
		t.Fatal(err)
	}
	if got := f.task("gmail.urgent").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 0 8-19 * * 1-5" {
		t.Fatalf("urgent = %s", got)
	}
	if f.task("gmail.evening") != evening {
		t.Fatal("window change touched a fixed-time preset")
	}
	if err := f.svc.SetWindow(ctx, Window{StartHour: 20, EndHour: 8}); err == nil {
		t.Fatal("invalid window accepted")
	}
	if f.svc.Window(ctx) != (Window{StartHour: 8, EndHour: 20}) {
		t.Fatal("rejected window was stored")
	}
}

func TestSettingsPersistAcrossServiceInstances(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	q, _ := ParseQuiet("2200", "0800")
	if err := f.svc.SetQuiet(ctx, &q); err != nil {
		t.Fatal(err)
	}
	if err := f.svc.SetWindow(ctx, Window{StartHour: 10, EndHour: 18}); err != nil {
		t.Fatal(err)
	}
	g := f.service()
	if got := g.Quiet(ctx); got == nil || *got != q || g.Window(ctx) != (Window{10, 18}) {
		t.Fatalf("not persisted: quiet=%v window=%v", g.Quiet(ctx), g.Window(ctx))
	}
}
