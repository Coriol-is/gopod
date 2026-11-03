package proactive

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// makeAvailable lifts the availability gate of a preset for one test, so the
// poll-rendering logic that PR 4 will rely on stays covered.
func makeAvailable(t *testing.T, id string) {
	t.Helper()
	for i := range catalog {
		if catalog[i].ID == id {
			old := catalog[i]
			catalog[i].unavailable = ""
			t.Cleanup(func() { catalog[i] = old })
			return
		}
	}
	t.Fatalf("no preset %s", id)
}

func TestOnlyCalReminderIsUnavailableUntilTimedRemindersExist(t *testing.T) {
	for _, p := range Catalog() {
		reason, off := p.Unavailable()
		if p.ID == "cal.reminder" {
			if !off || !strings.Contains(reason, "later update") {
				t.Errorf("cal.reminder: Unavailable() = %q, %v", reason, off)
			}
			continue
		}
		if off {
			t.Errorf("%s must be available", p.ID)
		}
	}
}

func TestEnableRefusesAnUnavailablePreset(t *testing.T) {
	f := newFixture(t)
	err := f.svc.Enable(context.Background(), "cal.reminder")
	if !errors.Is(err, ErrPresetUnavailable) {
		t.Fatalf("err = %v, want ErrPresetUnavailable", err)
	}
	if f.taskCount() != 0 {
		t.Fatal("a refused enable created a task")
	}
	for _, id := range []string{"gmail.morning", "gmail.urgent", "gmail.evening", "cal.agenda", "cal.weekly"} {
		f.enable(id)
	}
	if f.taskCount() != 5 {
		t.Fatalf("tasks = %d, want 5", f.taskCount())
	}
}

func TestUpgradeTemplatesNeverRewritesAnUnavailablePreset(t *testing.T) {
	f := newFixture(t)
	makeAvailable(t, "cal.reminder") // as if enabled by an earlier build
	f.enable("cal.reminder")
	before := f.task("cal.reminder")
	for i := range catalog { // unavailable again, newer template
		if catalog[i].ID == "cal.reminder" {
			catalog[i].unavailable = "x later update"
			catalog[i].TemplateVersion++
			catalog[i].body += " NEW"
		}
	}
	res, err := f.svc.UpgradeTemplates(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.PresetID == "cal.reminder" && r.Outcome != UpgradePausedUnavailable {
			t.Fatalf("upgrade rewrote an unavailable preset: %+v", r)
		}
	}
	after := f.task("cal.reminder")
	if after.Prompt != before.Prompt || after.ScheduleValue != before.ScheduleValue {
		t.Fatalf("definition changed: %+v -> %+v", before, after)
	}
}

// liveUnavailable leaves cal.reminder as a live task from "an earlier build"
// and then closes the gate again.
func (f *fixture) liveUnavailable() {
	f.t.Helper()
	makeAvailable(f.t, "cal.reminder")
	f.enable("cal.reminder")
	for i := range catalog {
		if catalog[i].ID == "cal.reminder" {
			catalog[i].unavailable = "timed reminders arrive in a later update"
		}
	}
}

func TestServiceOperationsRefuseAnUnavailablePreset(t *testing.T) {
	f := newFixture(t)
	f.liveUnavailable()
	before := f.task("cal.reminder")
	ctx := context.Background()
	if err := f.svc.Configure(ctx, "cal.reminder", Settings{Freq: "30m", Lead: 60}); !errors.Is(err, ErrPresetUnavailable) {
		t.Errorf("Configure: %v", err)
	}
	if err := f.svc.UpdateSettings(ctx, "cal.reminder", func(*Settings) error { return nil }); !errors.Is(err, ErrPresetUnavailable) {
		t.Errorf("UpdateSettings: %v", err)
	}
	if err := f.svc.ResetToTemplate(ctx, "cal.reminder"); !errors.Is(err, ErrPresetUnavailable) {
		t.Errorf("ResetToTemplate: %v", err)
	}
	if after := f.task("cal.reminder"); after != before {
		t.Fatalf("task changed: %+v -> %+v", before, after)
	}
}

func TestSetTimezoneAndResumeAllLeaveAnUnavailablePresetAlone(t *testing.T) {
	f := newFixture(t)
	f.enable("gmail.morning")
	f.liveUnavailable()
	ctx := context.Background()
	before := f.task("cal.reminder")
	if _, err := f.svc.SetTimezone(ctx, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	if after := f.task("cal.reminder"); after.ScheduleValue != before.ScheduleValue || after.Prompt != before.Prompt {
		t.Fatalf("SetTimezone rendered an unavailable preset: %q", after.ScheduleValue)
	}
	if got := f.task("gmail.morning").ScheduleValue; !strings.Contains(got, "Asia/Tokyo") {
		t.Fatalf("available preset not moved: %s", got)
	}
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := f.svc.ResumeAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.task("cal.reminder"); got.Status != "paused" {
		t.Fatalf("ResumeAll reactivated an unavailable preset: %s", got.Status)
	}
	if !containsString(rep.Skipped, "cal.reminder") || !containsString(rep.Resumed, "gmail.morning") {
		t.Fatalf("report = %+v", rep)
	}
}

func TestUpgradeTemplatesPausesALiveUnavailablePresetOnce(t *testing.T) {
	f := newFixture(t)
	f.liveUnavailable()
	ctx := context.Background()
	res, err := f.svc.UpgradeTemplates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var got []UpgradeResult
	for _, r := range res {
		if r.PresetID == "cal.reminder" {
			got = append(got, r)
		}
	}
	if len(got) != 1 || got[0].Outcome != UpgradePausedUnavailable {
		t.Fatalf("results = %+v", res)
	}
	if tk := f.task("cal.reminder"); tk.Status != "paused" {
		t.Fatalf("status = %s", tk.Status)
	}
	if st := f.status("cal.reminder"); st.PausedReason == "" {
		t.Error("the pause has no reason on the panel")
	}
	res, err = f.svc.UpgradeTemplates(ctx)
	if err != nil || len(res) != 0 {
		t.Fatalf("second run reported again: %+v %v", res, err)
	}
}
