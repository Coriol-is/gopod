package proactive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	taskIDPrefix = "preset-"
	reminderID   = "cal.reminder"

	// ReminderMarker starts the prompt of every cal.reminder one-shot task, so
	// the host can find one whose agent-chosen id lacks the preset prefix.
	ReminderMarker = "[gopod:cal.reminder]"
)

// TaskID is the deterministic scheduled_tasks id of a preset.
func TaskID(presetID string) string { return taskIDPrefix + presetID }

// ContentHash is the digest gopod stores as applied_hash. A task whose current
// prompt, schedule type or schedule value hash differently was edited outside
// gopod (an IPC update that changes only the type counts). The length prefixes keep the fields from sliding into each other.
func ContentHash(prompt, scheduleType, scheduleValue string) string {
	h := sha256.New()
	for _, f := range []string{prompt, scheduleType, scheduleValue} {
		fmt.Fprintf(h, "%d:%s\x00", len(f), f) // length-prefixed: no field content can shift a boundary
	}
	return hex.EncodeToString(h.Sum(nil))
}

// ReminderOneShotID is the deterministic id of the one-shot reminder for an
// event: the first 12 hex digits of sha256("<event>|<start UTC RFC3339>|<lead>").
func ReminderOneShotID(eventID string, start time.Time, lead int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", eventID, start.UTC().Format(time.RFC3339), lead)))
	return taskIDPrefix + reminderID + "-" + hex.EncodeToString(sum[:])[:12]
}

// IsReminderOneShot reports whether a task is a cal.reminder one-shot: in the
// owner folder, schedule type once, and either the id prefix or the marker at
// the start of the prompt. Folder and type are checked here because an id and a
// prompt are agent-chosen text; the poll task itself (id preset-cal.reminder,
// type cron) is not a one-shot.
func IsReminderOneShot(t store.TaskRecord, ownerFolder string) bool {
	if t.ChatFolder != ownerFolder || t.ScheduleType != "once" {
		return false
	}
	return strings.HasPrefix(t.ID, taskIDPrefix+reminderID+"-") || strings.HasPrefix(t.Prompt, ReminderMarker)
}
