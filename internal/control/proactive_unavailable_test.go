package control

import (
	"strings"
	"testing"
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
