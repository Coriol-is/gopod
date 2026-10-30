package proactive

import (
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func TestTaskIDIsDeterministic(t *testing.T) {
	if got := TaskID("gmail.morning"); got != "preset-gmail.morning" {
		t.Fatalf("got %q", got)
	}
}

func TestContentHashCoversPromptTypeAndSchedule(t *testing.T) {
	a := ContentHash("p", "cron", "s")
	if a != ContentHash("p", "cron", "s") || len(a) != 64 {
		t.Fatalf("hash unstable or wrong length: %q", a)
	}
	// The separator must keep neighbouring fields apart.
	if ContentHash("a\x00b", "cron", "c") == ContentHash("a", "b\x00cron", "c") ||
		ContentHash("a", "cron", "b") == ContentHash("a", "cron\x00b", "") {
		t.Fatal("hash is ambiguous across a field boundary")
	}
	if a == ContentHash("p", "cron", "s2") || a == ContentHash("p2", "cron", "s") {
		t.Fatal("hash ignores a field")
	}
	// An owner IPC update that changes only the schedule type is an edit.
	if a == ContentHash("p", "interval", "s") {
		t.Fatal("hash ignores schedule_type")
	}
}

func TestReminderOneShotIDs(t *testing.T) {
	start := time.Date(2026, 11, 2, 10, 0, 0, 0, time.FixedZone("x", 3600))
	id := ReminderOneShotID("evt1", start, 10)
	if !strings.HasPrefix(id, "preset-cal.reminder-") || len(id) != len("preset-cal.reminder-")+12 {
		t.Fatalf("shape: %q", id)
	}
	if id != ReminderOneShotID("evt1", start.UTC(), 10) {
		t.Fatal("id must depend on the instant, not the zone it is expressed in")
	}
	if id == ReminderOneShotID("evt1", start, 15) || id == ReminderOneShotID("evt1", start.Add(time.Minute), 10) || id == ReminderOneShotID("evt2", start, 10) {
		t.Fatal("id must change with lead, start and event")
	}
	oneShot := func(id, folder, typ, prompt string) store.TaskRecord {
		return store.TaskRecord{ID: id, ChatFolder: folder, ScheduleType: typ, Prompt: prompt}
	}
	if !IsReminderOneShot(oneShot(id, "owner", "once", "x"), "owner") {
		t.Fatal("prefix not recognised")
	}
	for name, tk := range map[string]store.TaskRecord{
		"poll task":       oneShot("preset-cal.reminder", "owner", "cron", "p"),
		"other preset":    oneShot("preset-cal.agenda", "owner", "cron", "p"),
		"foreign id":      oneShot("other", "owner", "once", "p"),
		"foreign folder":  oneShot(id, "alice", "once", "x"),
		"not a once task": oneShot(id, "owner", "cron", "x"),
		"forged marker":   oneShot("agent-chosen", "alice", "once", ReminderMarker+" x"),
		"marker not once": oneShot("agent-chosen", "owner", "interval", ReminderMarker+" x"),
	} {
		if IsReminderOneShot(tk, "owner") {
			t.Errorf("%s taken for a one-shot", name)
		}
	}
	if !IsReminderOneShot(oneShot("agent-chosen", "owner", "once", ReminderMarker+" Reminder check. Event id e."), "owner") {
		t.Fatal("marker fallback not recognised for an owner-folder once task")
	}
	if IsReminderOneShot(oneShot("agent-chosen", "owner", "once", "text "+ReminderMarker), "owner") {
		t.Fatal("the marker must start the prompt")
	}
}
