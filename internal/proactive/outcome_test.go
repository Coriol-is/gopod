package proactive

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestParseOutcomeReadsTheAgentFile(t *testing.T) {
	o, err := ParseOutcome([]byte(`{"at":"2026-10-30T14:00:12+01:00","result":"sent","note":"3 items"}`))
	if err != nil {
		t.Fatal(err)
	}
	if o.Result != OutcomeSent || o.Note != "3 items" || !o.At.Equal(time.Date(2026, 10, 30, 13, 0, 12, 0, time.UTC)) {
		t.Fatalf("outcome = %+v", o)
	}
	if !o.Spoke() {
		t.Error("sent outcome should count as spoke")
	}
}

func TestParseOutcomeClassifiesResults(t *testing.T) {
	for in, want := range map[string]Result{
		"sent": OutcomeSent, "empty": OutcomeEmpty, "unavailable": OutcomeUnavailable, "error": OutcomeError,
		"SENT": OutcomeUnknown, "ok": OutcomeUnknown, "": OutcomeUnknown,
	} {
		o, err := ParseOutcome([]byte(`{"result":"` + in + `"}`))
		if err != nil || o.Result != want {
			t.Errorf("result %q -> %v, %v; want %v", in, o.Result, err, want)
		}
	}
}

// The file is written by an agent that read attacker-controlled mail, so the
// note is data to display: single line, no control characters, no backticks,
// at most 120 characters.
func TestParseOutcomeSanitizesTheNote(t *testing.T) {
	note := "line1\nline2\x1b[31m `code` <b>x</b> " + strings.Repeat("ж", 200)
	o, err := ParseOutcome(mustJSON(t, map[string]string{"result": "sent", "note": note}))
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(o.Note, "\n\r\x1b`") {
		t.Errorf("unsafe characters survive: %q", o.Note)
	}
	if n := len([]rune(o.Note)); n > 120 {
		t.Errorf("note has %d runes", n)
	}
}

func TestParseOutcomeRejectsGarbage(t *testing.T) {
	for _, in := range []string{``, `not json`, `[]`, `{"result":`, strings.Repeat("a", 9000)} {
		if _, err := ParseOutcome([]byte(in)); err == nil {
			t.Errorf("accepted %.20q", in)
		}
	}
}

func TestParseOutcomeBadTimeKeepsTheResult(t *testing.T) {
	o, err := ParseOutcome([]byte(`{"at":"yesterday","result":"empty"}`))
	if err != nil || o.Result != OutcomeEmpty || !o.At.IsZero() {
		t.Fatalf("got %+v, %v", o, err)
	}
}

func TestReadOutcome(t *testing.T) {
	dir := t.TempDir()
	if _, err := ReadOutcome(dir, "gmail.urgent"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("missing file: err = %v, want not-exist", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "proactive"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "proactive", "gmail.urgent.last"), []byte(`{"result":"empty"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	o, err := ReadOutcome(dir, "gmail.urgent")
	if err != nil || o.Result != OutcomeEmpty {
		t.Fatalf("got %+v, %v", o, err)
	}
	// Only catalog ids name a file; nothing the caller passes can escape the folder.
	if _, err := ReadOutcome(dir, "../../etc/passwd"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("path traversal not rejected: %v", err)
	}
}

func TestReadOutcomeIgnoresASymlinkAndHugeFile(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, "proactive")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	secret := filepath.Join(dir, "secret")
	if err := os.WriteFile(secret, []byte(`{"result":"sent"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(secret, filepath.Join(pd, "gmail.morning.last")); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOutcome(dir, "gmail.morning"); err == nil {
		t.Error("followed a symlink")
	}
	if err := os.WriteFile(filepath.Join(pd, "cal.agenda.last"), []byte(strings.Repeat("x", 1<<20)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadOutcome(dir, "cal.agenda"); err == nil {
		t.Error("read a megabyte file")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
