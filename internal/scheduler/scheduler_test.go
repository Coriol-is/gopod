package scheduler

import (
	"testing"
	"time"

	"github.com/spaceinvaderz/gopod/internal/store"
)

func TestComputeNextRunCron(t *testing.T) {
	now := time.Now().UnixMilli()
	task := store.TaskRecord{ScheduleType: "cron", ScheduleValue: "* * * * *"} // every minute

	next, status := computeNextRun(task, now)
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
	if next <= now {
		t.Errorf("next_run %d should be > now %d", next, now)
	}
	// "every minute" cron — next should be within 60s of now.
	if next-now > 61_000 {
		t.Errorf("next_run %d is more than 61s after now %d", next, now)
	}
}

func TestComputeNextRunInterval(t *testing.T) {
	now := int64(1000000)
	task := store.TaskRecord{ScheduleType: "interval", ScheduleValue: "1h"}

	next, status := computeNextRun(task, now)
	if status != "active" {
		t.Errorf("status = %q", status)
	}
	expected := now + time.Hour.Milliseconds()
	if next != expected {
		t.Errorf("next = %d, want %d", next, expected)
	}
}

func TestComputeNextRunOnce(t *testing.T) {
	task := store.TaskRecord{ScheduleType: "once"}
	next, status := computeNextRun(task, 1000)
	if status != "done" || next != 0 {
		t.Errorf("once: next=%d status=%q, want 0/done", next, status)
	}
}

func TestValidateCron(t *testing.T) {
	if err := ValidateCron("0 9 * * *"); err != nil {
		t.Errorf("valid cron rejected: %v", err)
	}
	if err := ValidateCron("not a cron"); err == nil {
		t.Error("invalid cron accepted")
	}
}

func TestValidateInterval(t *testing.T) {
	if err := ValidateInterval("1h30m"); err != nil {
		t.Errorf("valid interval rejected: %v", err)
	}
	if err := ValidateInterval("banana"); err == nil {
		t.Error("invalid interval accepted")
	}
}
