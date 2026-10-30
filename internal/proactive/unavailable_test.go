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

func TestUpgradeTemplatesLeavesAnUnavailablePresetAlone(t *testing.T) {
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
		if r.PresetID == "cal.reminder" {
			t.Fatalf("upgrade touched an unavailable preset: %+v", r)
		}
	}
	if after := f.task("cal.reminder"); after != before {
		t.Fatalf("task changed: %+v -> %+v", before, after)
	}
}
