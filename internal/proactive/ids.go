package proactive

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
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
// prompt and schedule hash differently was edited outside gopod. The 0x00
// separator keeps the two fields from sliding into each other.
func ContentHash(prompt, scheduleValue string) string {
	sum := sha256.Sum256([]byte(prompt + "\x00" + scheduleValue))
	return hex.EncodeToString(sum[:])
}

// ReminderOneShotID is the deterministic id of the one-shot reminder for an
// event: the first 12 hex digits of sha256("<event>|<start UTC RFC3339>|<lead>").
func ReminderOneShotID(eventID string, start time.Time, lead int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%d", eventID, start.UTC().Format(time.RFC3339), lead)))
	return taskIDPrefix + reminderID + "-" + hex.EncodeToString(sum[:])[:12]
}

// IsReminderOneShot reports whether a task is a cal.reminder one-shot: by the
// id prefix first, by the marker at the start of the prompt as a fallback.
// The poll task itself (id preset-cal.reminder) is not a one-shot.
func IsReminderOneShot(taskID, prompt string) bool {
	return strings.HasPrefix(taskID, taskIDPrefix+reminderID+"-") || strings.HasPrefix(prompt, ReminderMarker)
}
