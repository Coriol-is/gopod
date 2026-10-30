package control

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ranAt records that the preset's task last ran at the given UTC time.
func (fx *proactiveFx) ranAt(id string, at time.Time) {
	fx.t.Helper()
	tk := fx.task(id)
	if err := fx.st.UpdateTaskStatus(t0(fx.t), tk.ID, tk.Status, tk.NextRun, at.UnixMilli()); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *proactiveFx) writeOutcome(id, body string) {
	fx.t.Helper()
	dir := filepath.Join(fx.chatDir, "proactive")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, id+".last"), []byte(body), 0o644); err != nil {
		fx.t.Fatal(err)
	}
}

var lastRunUTC = time.Date(2026, 10, 30, 9, 0, 0, 0, time.UTC) // 10:00 in Europe/Belgrade (Fri)

func TestPresetScreenShowsWhatTheLastRunDid(t *testing.T) {
	cases := []struct {
		name, file string
		want       []string
	}{
		{"sent", `{"at":"2026-10-30T09:00:40Z","result":"sent","note":"3 items"}`, []string{"Last outcome: sent a message", "Fri 10:00", "3 items"}},
		{"empty", `{"at":"2026-10-30T09:00:40Z","result":"empty","note":"nothing new"}`, []string{"Last outcome: stayed silent (nothing to report)", "nothing new"}},
		{"unavailable", `{"at":"2026-10-30T09:00:40Z","result":"unavailable","note":"connector not connected"}`, []string{"Last outcome: connector not available", "connector not connected"}},
		{"error", `{"at":"2026-10-30T09:00:40Z","result":"error","note":"timeout"}`, []string{"Last outcome: error", "timeout"}},
		{"unrecognized result", `{"at":"2026-10-30T09:00:40Z","result":"great"}`, []string{"Last outcome: ran, outcome unknown"}},
		{"file from an earlier run", `{"at":"2026-10-28T09:00:40Z","result":"sent"}`, []string{"Last outcome: ran, outcome unknown"}},
		{"corrupt file", `{{{`, []string{"Last outcome: ran, outcome unknown"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := newProactiveFx(t)
			fx.enable("gmail", "urgent", "on")
			fx.ranAt("gmail.urgent", lastRunUTC)
			fx.writeOutcome("gmail.urgent", tc.file)
			got := fx.run("gmail", "urgent").Text
			for _, w := range tc.want {
				if !strings.Contains(got, w) {
					t.Errorf("screen lacks %q:\n%s", w, got)
				}
			}
		})
	}
}

func TestPresetScreenWithoutOutcomeFile(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "urgent", "on")
	if got := fx.run("gmail", "urgent").Text; strings.Contains(got, "Last outcome") {
		t.Errorf("a preset that never ran shows an outcome:\n%s", got)
	}
	fx.ranAt("gmail.urgent", lastRunUTC)
	if got := fx.run("gmail", "urgent").Text; !strings.Contains(got, "Last outcome: ran, outcome unknown") {
		t.Errorf("a run without an outcome file must say so:\n%s", got)
	}
}

func TestPresetScreenTreatsTheOutcomeNoteAsUntrusted(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "urgent", "on")
	fx.ranAt("gmail.urgent", lastRunUTC)
	fx.writeOutcome("gmail.urgent", `{"at":"2026-10-30T09:00:40Z","result":"sent","note":"<b>bold</b> *x* [link](http://evil) `+"`"+`tick"}`)
	got := fx.run("gmail", "urgent").Text
	for _, bad := range []string{"<b>", "[link]", "*x*"} {
		if strings.Contains(got, bad) {
			t.Errorf("markup %q passed through:\n%s", bad, got)
		}
	}
}
