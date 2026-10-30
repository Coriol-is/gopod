package proactive

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode"
)

// Result is the outcome class an agent reports in a preset's outcome file.
type Result string

const (
	OutcomeSent        Result = "sent"
	OutcomeEmpty       Result = "empty"
	OutcomeUnavailable Result = "unavailable"
	OutcomeError       Result = "error"
	OutcomeUnknown     Result = "" // missing, unreadable or an unrecognized value
)

const (
	outcomeMaxBytes = 4096 // the file is tiny by contract; anything bigger is not ours
	outcomeMaxNote  = 120  // runes
)

// Outcome is the content of /workspace/chat/proactive/<preset id>.last
// (section 5.1). It is written by the agent after reading attacker-controlled
// mail and calendar text, so it is display text only: nothing in gopod acts on it.
type Outcome struct {
	At     time.Time // zero when the agent wrote none or an invalid time
	Result Result
	// Modified is the modification time of the file as opened (zero from ParseOutcome).
	Modified time.Time
	Note     string // sanitized, at most 120 runes, one line, no backticks
}

// Spoke reports whether the run sent a message to the owner.
func (o Outcome) Spoke() bool { return o.Result == OutcomeSent }

// ParseOutcome reads an outcome file. A bad time keeps the rest; an
// unrecognized result becomes OutcomeUnknown.
func ParseOutcome(data []byte) (Outcome, error) {
	if len(data) > outcomeMaxBytes {
		return Outcome{}, fmt.Errorf("outcome file is %d bytes, over %d", len(data), outcomeMaxBytes)
	}
	var raw struct {
		At     string `json:"at"`
		Result string `json:"result"`
		Note   string `json:"note"`
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return Outcome{}, fmt.Errorf("parsing outcome file: %w", err)
	}
	o := Outcome{Note: sanitizeNote(raw.Note)}
	if at, err := time.Parse(time.RFC3339, raw.At); err == nil {
		o.At = at
	}
	switch r := Result(raw.Result); r {
	case OutcomeSent, OutcomeEmpty, OutcomeUnavailable, OutcomeError:
		o.Result = r
	}
	return o, nil
}

// sanitizeNote makes agent text safe to show: one line, printable, no
// backticks (the screen shows it inside a code span), capped.
func sanitizeNote(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r == '`':
			return '\''
		case unicode.IsSpace(r):
			return ' '
		case unicode.IsControl(r) || !unicode.IsPrint(r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > outcomeMaxNote {
		s = string(r[:outcomeMaxNote])
	}
	return s
}

// ReadOutcome reads the outcome file of a catalog preset from the host side of
// the chat workspace (chatDir is the folder mounted at /workspace/chat). The
// folder is writable by an agent that may have read hostile mail. What is
// enforced:
//
//   - the lookup runs in an os.Root over chatDir, so no path component can
//     leave it;
//   - "proactive" and the file are checked with Lstat: symlinks are refused
//     even when they stay inside the workspace;
//   - the open uses O_NOFOLLOW, O_NOCTTY and O_NONBLOCK (as far as the
//     platform has them), so a symlink or FIFO swapped in after the check is
//     not followed and cannot block;
//   - the opened descriptor is then checked: it must be a regular file, the
//     same file the path names now (os.SameFile against a fresh Lstat), and
//     have a single hard link. os.Root alone does not stop opening a device
//     node or following a symlink inside the root, hence these checks;
//   - the read is capped at 4 KiB.
//
// A missing file returns an error wrapping fs.ErrNotExist.
func ReadOutcome(chatDir, presetID string) (Outcome, error) {
	return readOutcome(chatDir, presetID, nil)
}

// readOutcome is ReadOutcome with a hook that runs between the type check and
// the open, so tests can swap the file there; production passes nil.
func readOutcome(chatDir, presetID string, beforeOpen func()) (Outcome, error) {
	if _, ok := Lookup(presetID); !ok {
		return Outcome{}, fmt.Errorf("outcome: %q is not a preset", presetID)
	}
	root, err := os.OpenRoot(chatDir)
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: %w", err)
	}
	defer root.Close()
	dirInfo, err := root.Lstat("proactive")
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: %w", err)
	}
	if !dirInfo.IsDir() {
		return Outcome{}, fmt.Errorf("outcome: proactive is not a directory")
	}
	name := "proactive/" + presetID + ".last"
	info, err := root.Lstat(name)
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: %w", err)
	}
	if !info.Mode().IsRegular() {
		return Outcome{}, fmt.Errorf("outcome: %s is not a regular file", presetID)
	}
	if beforeOpen != nil {
		beforeOpen()
	}
	f, err := root.OpenFile(name, openFlags, 0)
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: %w", err)
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: %w", err)
	}
	if !fi.Mode().IsRegular() {
		return Outcome{}, fmt.Errorf("outcome: %s is not a regular file", presetID)
	}
	if lst, lerr := root.Lstat(name); lerr != nil || !lst.Mode().IsRegular() || !os.SameFile(fi, lst) {
		return Outcome{}, fmt.Errorf("outcome: %s changed while it was being opened", presetID)
	}
	if hasExtraLinks(fi) {
		return Outcome{}, fmt.Errorf("outcome: %s is a hard link", presetID)
	}
	data, err := io.ReadAll(io.LimitReader(f, outcomeMaxBytes+1))
	if err != nil {
		return Outcome{}, fmt.Errorf("outcome: reading %s: %w", presetID, err)
	}
	o, err := ParseOutcome(data)
	if err != nil {
		return Outcome{}, err
	}
	o.Modified = fi.ModTime()
	return o, nil
}
