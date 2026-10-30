package control

import (
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/proactive"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestReminderCannotBeEnabledYet(t *testing.T) {
	fx := newProactiveFx(t)
	for _, confirmed := range []bool{false, true} {
		var resp Response
		if confirmed {
			resp = fx.confirmed("calendar", "reminder", "on")
		} else {
			resp = fx.run("calendar", "reminder", "on")
		}
		if resp.Code != 1 || !strings.Contains(resp.Text, "not available yet") || !strings.Contains(resp.Text, "later update") {
			t.Fatalf("confirmed=%v: %+v", confirmed, resp)
		}
		if resp.Code == CodeConfirm {
			t.Fatal("asked a confirmation for a preset that cannot be enabled")
		}
	}
	if fx.hasTask("cal.reminder") {
		t.Fatal("a task was created for an unavailable preset")
	}
}

func TestReminderScreenSaysUnavailableAndOffersNoSwitch(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run("calendar", "reminder")
	if !strings.Contains(resp.Text, "Not available yet") || !strings.Contains(resp.Text, "later update") {
		t.Fatalf("screen: %s", resp.Text)
	}
	for _, l := range labels(resp) {
		if l == "On" || l == "Off" || strings.HasPrefix(l, "✓") || strings.HasSuffix(l, " min") {
			t.Errorf("unavailable preset offers %q", l)
		}
	}
	if c := fx.run("calendar").Text; !strings.Contains(c, "meeting reminders — not available yet") {
		t.Fatalf("connector screen: %s", c)
	}
}

func TestOtherPresetsStillEnable(t *testing.T) {
	fx := newProactiveFx(t)
	for _, a := range [][]string{{"gmail", "morning"}, {"gmail", "urgent"}, {"gmail", "evening"}, {"calendar", "agenda"}, {"calendar", "weekly"}} {
		fx.enable(append(a, "on")...)
	}
	if got := fx.run("calendar", "agenda").Text; strings.Contains(got, "Not available") {
		t.Fatalf("agenda marked unavailable: %s", got)
	}
}

// A cal.reminder task left by an earlier build keeps the unavailable notice on
// the screen whatever its state, and can still be turned off.
func TestUnavailableNoticeShowsForALiveTaskToo(t *testing.T) {
	fx := newProactiveFx(t)
	id := proactive.TaskID("cal.reminder")
	if _, err := fx.st.CreateTask(t.Context(), store.TaskRecord{
		ID: id, ChatFolder: "owner", ChatJID: "tg:100", Prompt: "old poll",
		ScheduleType: "cron", ScheduleValue: "CRON_TZ=Europe/Belgrade */30 9-18 * * 1-5", NextRun: 1893456000000, Status: "active", CreatedAt: 1,
	}); err != nil {
		t.Fatal(err)
	}
	if err := fx.st.PutProactivePreset(t.Context(), store.ProactivePreset{PresetID: "cal.reminder", TaskID: id, Settings: `{"freq":"30m","lead":10}`, AppliedHash: proactive.ContentHash("old poll", "cron", "CRON_TZ=Europe/Belgrade */30 9-18 * * 1-5"), UpdatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	resp := fx.run("calendar", "reminder")
	if !strings.Contains(resp.Text, "Not available yet") {
		t.Fatalf("a live unavailable preset hides the notice:\n%s", resp.Text)
	}
	for _, l := range labels(resp) {
		if l == "On" || l == "✓ On" || strings.HasSuffix(l, " min") {
			t.Errorf("offers %q", l)
		}
	}
	if off := fx.run("calendar", "reminder", "off"); off.Code != 0 {
		t.Fatalf("turning a live unavailable preset off must work: %+v", off)
	}
}

func TestInvalidValueMessagesForLeadsAreListedPlainly(t *testing.T) {
	p, _ := proactive.Lookup("cal.reminder")
	got := invalidValue(p, "lead", "20")
	if !strings.Contains(got, "10, 15, 30, 60") || strings.ContainsAny(got, "[]") {
		t.Fatalf("lead message: %q", got)
	}
}
