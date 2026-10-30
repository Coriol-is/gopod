package proactive

import (
	"strings"
	"testing"
	"time"
)

func TestTaskIDIsDeterministic(t *testing.T) {
	if got := TaskID("gmail.morning"); got != "preset-gmail.morning" {
		t.Fatalf("got %q", got)
	}
}

func TestContentHashCoversPromptAndSchedule(t *testing.T) {
	a := ContentHash("p", "s")
	if a != ContentHash("p", "s") || len(a) != 64 {
		t.Fatalf("hash unstable or wrong length: %q", a)
	}
	// The separator must keep ("a|b","c") apart from ("a","b|c").
	if ContentHash("a|b", "c") == ContentHash("a", "b|c") {
		t.Fatal("hash is ambiguous across the prompt/schedule boundary")
	}
	if a == ContentHash("p", "s2") || a == ContentHash("p2", "s") {
		t.Fatal("hash ignores a field")
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
	if !IsReminderOneShot(id, "anything") {
		t.Fatal("prefix not recognised")
	}
	if IsReminderOneShot("preset-cal.reminder", "p") || IsReminderOneShot("preset-cal.agenda", "p") || IsReminderOneShot("other", "p") {
		t.Fatal("poll task or foreign task taken for a one-shot")
	}
	if !IsReminderOneShot("agent-chose-this", ReminderMarker+" Reminder check. Event id e.") {
		t.Fatal("marker fallback not recognised")
	}
	if IsReminderOneShot("agent-chose-this", "text "+ReminderMarker) {
		t.Fatal("the marker must start the prompt")
	}
}
