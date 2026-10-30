package proactive

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

func (f *fixture) paused() bool {
	f.t.Helper()
	on, err := f.svc.GloballyPaused(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return on
}

func (f *fixture) tz() string { return mustTZ(f.t, f.svc) }

func mustTZ(t *testing.T, svc *Service) string {
	t.Helper()
	tz, err := svc.Timezone(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return tz
}

func (f *fixture) mustWindow() Window {
	f.t.Helper()
	w, err := f.svc.Window(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return w
}

func (f *fixture) resume() ResumeReport {
	f.t.Helper()
	r, err := f.svc.ResumeAll(context.Background())
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) setTask(id string, mutate func(*store.TaskRecord)) {
	f.t.Helper()
	tk := f.task(id)
	mutate(&tk)
	if err := f.st.UpdateTaskDefinition(context.Background(), tk); err != nil {
		f.t.Fatal(err)
	}
}

// --- critical 1: a foreign task under a preset id is never adopted ---

func TestForeignTaskUnderPresetIDIsNeverAdopted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	for _, foreign := range []store.TaskRecord{
		{ChatFolder: "group", ChatJID: "tg:-9"},
		{ChatFolder: "owner", ChatJID: "tg:-9"}, // right folder, foreign chat
		{ChatFolder: "group", ChatJID: "tg:1"},  // right chat, foreign folder
	} {
		foreign.ID, foreign.Prompt, foreign.ScheduleType, foreign.ScheduleValue = TaskID("gmail.morning"), "attacker prompt", "cron", "0 9 * * *"
		foreign.NextRun, foreign.Status = 1, "active"
		if _, err := f.st.CreateTask(ctx, foreign); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.Enable(ctx, "gmail.morning"); !errors.Is(err, ErrTaskConflict) {
			t.Fatalf("Enable adopted %+v: %v", foreign, err)
		}
		if s := f.status("gmail.morning"); !s.Conflict || s.State != StateOff {
			t.Fatalf("status = %+v", s)
		}
		// Nothing else may touch it either.
		if err := f.svc.Disable(ctx, "gmail.morning"); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.PauseAll(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := f.svc.SetTimezone(ctx, "UTC"); err != nil {
			t.Fatal(err)
		}
		f.resume()
		if _, err := f.svc.SetTimezone(ctx, "Europe/Belgrade"); err != nil {
			t.Fatal(err)
		}
		got := f.task("gmail.morning")
		if got.Prompt != "attacker prompt" || got.Status != "active" || got.NextRun != 1 || got.ScheduleValue != "0 9 * * *" {
			t.Fatalf("foreign task modified: %+v", got)
		}
		if err := f.st.DeleteTask(ctx, TaskID("gmail.morning")); err != nil {
			t.Fatal(err)
		}
	}
}

// --- critical 2: owner-edited non-cron tasks keep their type ---

func TestSetTimezoneLeavesEditedNonCronTasksAlone(t *testing.T) {
	for _, paused := range []bool{false, true} {
		f := newFixture(t)
		ctx := context.Background()
		f.enable("gmail.morning")
		f.enable("cal.agenda")
		if paused {
			if err := f.svc.Disable(ctx, "gmail.morning"); err != nil {
				t.Fatal(err)
			}
		}
		f.setTask("gmail.morning", func(tk *store.TaskRecord) { tk.ScheduleType, tk.ScheduleValue = "interval", "2h" })
		rep, err := f.svc.SetTimezone(ctx, "America/New_York")
		if err != nil {
			t.Fatalf("paused=%v: zone change failed: %v", paused, err)
		}
		tk := f.task("gmail.morning")
		if tk.ScheduleType != "interval" || tk.ScheduleValue != "2h" {
			t.Fatalf("paused=%v: edited schedule changed: %+v", paused, tk)
		}
		kept := strings.Join(rep.Kept, ",")
		if !strings.Contains(kept, "gmail.morning") {
			t.Fatalf("paused=%v: report = %+v", paused, rep)
		}
		if !strings.HasPrefix(f.task("cal.agenda").ScheduleValue, "CRON_TZ=America/New_York ") {
			t.Fatal("the zone change did not reach the other preset")
		}
		if paused { // and a later pause/resume cycle must not abort on it
			if err = f.svc.PauseAll(ctx); err != nil {
				t.Fatal(err)
			}
			f.resume()
		}
	}
}

func TestSetTimezoneKeepsEditedCronThatCannotBeMoved(t *testing.T) {
	f := newFixture(t)
	f.enable("gmail.morning")
	f.setTask("gmail.morning", func(tk *store.TaskRecord) { tk.ScheduleValue = "not a cron" })
	rep, err := f.svc.SetTimezone(context.Background(), "UTC")
	if err != nil || len(rep.Kept) != 1 {
		t.Fatalf("rep=%+v err=%v", rep, err)
	}
	if f.task("gmail.morning").ScheduleValue != "not a cron" {
		t.Fatal("invalid owner text rewritten")
	}
}

// --- critical 3 and risk 7: pause / resume against the current environment ---

func TestResumeAllRespectsQuietHoursSetDuringThePause(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning") // 08:00
	f.enable("gmail.urgent")
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	q, _ := ParseQuiet("0700", "0900")
	if err := f.svc.SetQuiet(ctx, &q); err != nil {
		t.Fatal(err)
	}
	rep := f.resume()
	m := f.task("gmail.morning")
	if m.Status != "paused" || m.NextRun != 0 {
		t.Fatalf("morning resumed into quiet hours: %+v", m)
	}
	if s := f.status("gmail.morning"); s.PausedReason != ReasonInsideQuiet {
		t.Fatalf("status %+v", s)
	}
	if len(rep.StillPaused) != 1 || rep.StillPaused[0] != "gmail.morning" || len(rep.Resumed) != 1 || rep.Resumed[0] != "gmail.urgent" {
		t.Fatalf("report = %+v", rep)
	}
	if f.task("gmail.urgent").Status != "active" {
		t.Fatal("unaffected preset not resumed")
	}
}

func TestSetTimezoneDuringThePauseKeepsTheResumeMarker(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.SetTimezone(ctx, "Asia/Tokyo"); err != nil {
		t.Fatal(err)
	}
	f.resume()
	tk := f.task("gmail.morning")
	if tk.Status != "active" || !strings.HasPrefix(tk.ScheduleValue, "CRON_TZ=Asia/Tokyo ") || tk.NextRun <= 0 {
		t.Fatalf("not restored in the new zone: %+v", tk)
	}
}

func TestResumeAllDoesNotReactivateWhatTheOwnerPausedMeanwhile(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	f.enable("cal.agenda")
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	// The owner resumes morning from /tasks, then pauses it again from /tasks.
	tk := f.task("gmail.morning")
	if err := f.st.UpdateTaskStatus(ctx, tk.ID, "active", time.Now().Add(time.Hour).UnixMilli(), tk.LastRun); err != nil {
		t.Fatal(err)
	}
	if err := f.st.UpdateTaskStatus(ctx, tk.ID, "paused", 0, tk.LastRun); err != nil {
		t.Fatal(err)
	}
	rep := f.resume()
	if f.task("gmail.morning").Status != "paused" {
		t.Fatal("ResumeAll reactivated a task the owner paused during the pause")
	}
	if f.task("cal.agenda").Status != "active" {
		t.Fatal("untouched task not resumed")
	}
	if len(rep.Skipped) != 1 || rep.Skipped[0] != "gmail.morning" {
		t.Fatalf("report = %+v", rep)
	}
}

func TestPauseAllWithNothingActiveStillBlocksEnable(t *testing.T) { // owner decision (b)
	f := newFixture(t)
	ctx := context.Background()
	if err := f.svc.PauseAll(ctx); err != nil {
		t.Fatal(err)
	}
	if !f.paused() {
		t.Fatal("pause not recorded")
	}
	if err := f.svc.Enable(ctx, "gmail.morning"); !errors.Is(err, ErrGloballyPaused) {
		t.Fatalf("enable under an empty pause: %v", err)
	}
	f.resume()
	f.enable("gmail.morning")
}

func TestEditedTaskIsNotSubjectToQuietHours(t *testing.T) { // owner decision (a)
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	f.editPrompt("gmail.morning", "owner version")
	if err := f.svc.Disable(ctx, "gmail.morning"); err != nil {
		t.Fatal(err)
	}
	q, _ := ParseQuiet("0700", "0900")
	if err := f.svc.SetQuiet(ctx, &q); err != nil {
		t.Fatal(err)
	}
	f.enable("gmail.morning") // reactivates the owner's definition by design
	if tk := f.task("gmail.morning"); tk.Status != "active" || tk.Prompt != "owner version" || tk.ScheduleValue != "CRON_TZ=Europe/Belgrade 0 8 * * *" {
		t.Fatalf("%+v", tk)
	}
	if _, err := f.svc.SetTimezone(ctx, "UTC"); err != nil { // zone changes still move an edited cron
		t.Fatal(err)
	}
}

// --- risk 4: lock reentrancy through the IPC context key ---

func TestServiceDoesNotDeadlockUnderTheIPCTaskLock(t *testing.T) {
	f := newFixture(t)
	done := make(chan error, 1)
	go func() {
		done <- scheduler.WithTaskLock(context.Background(), f.st, func(ctx context.Context) error {
			if err := f.svc.Enable(ctx, "gmail.morning"); err != nil {
				return err
			}
			if err := f.svc.PauseAll(ctx); err != nil {
				return err
			}
			_, err := f.svc.ResumeAll(ctx)
			return err
		})
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deadlock under the IPC lock key")
	}
}

// --- risk 8: quiet validation ---

func TestSetQuietValidates(t *testing.T) {
	f := newFixture(t)
	for _, q := range []Quiet{{-1, 60}, {60, 1440}, {1440, 60}, {600, 600}, {0, 0}} {
		q := q
		if err := f.svc.SetQuiet(context.Background(), &q); err == nil {
			t.Errorf("accepted %+v", q)
		}
	}
	if got, _ := f.svc.Quiet(context.Background()); got != nil {
		t.Fatalf("a rejected quiet window was stored: %+v", got)
	}
	ok := Quiet{StartMin: 22 * 60, EndMin: 8 * 60}
	if err := f.svc.SetQuiet(context.Background(), &ok); err != nil {
		t.Fatal(err)
	}
}

// --- risk 9: rerender writes the current template version ---

func TestRerenderMarksTheTemplateCurrent(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.urgent")
	bumpTemplate(t, "gmail.urgent", " NEW TEXT")
	if err := f.svc.SetWindow(ctx, Window{StartHour: 8, EndHour: 20}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.task("gmail.urgent").Prompt, "NEW TEXT") {
		t.Fatal("rerender did not use the current template")
	}
	if s := f.status("gmail.urgent"); s.TemplateOutdated {
		t.Fatal("rerender left the template marked outdated")
	}
	for i := 0; i < 2; i++ {
		if res, err := f.svc.UpgradeTemplates(ctx); err != nil || len(res) != 0 {
			t.Fatalf("pass %d reports %+v err=%v", i, res, err)
		}
	}
}

func TestUpgradeTemplatesBlockedByQuietHours(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	q, _ := ParseQuiet("0700", "0900") // pauses morning, leaves the task unmodified
	if err := f.svc.SetQuiet(ctx, &q); err != nil {
		t.Fatal(err)
	}
	bumpTemplate(t, "gmail.morning", " NEW TEXT")
	res, err := f.svc.UpgradeTemplates(ctx)
	if err != nil || len(res) != 1 || res[0].Outcome != UpgradeBlocked {
		t.Fatalf("res=%+v err=%v", res, err)
	}
	if strings.Contains(f.task("gmail.morning").Prompt, "NEW TEXT") {
		t.Fatal("blocked upgrade rewrote the task")
	}
}

// --- risk 10: read errors are not silent ---

func TestCorruptSettingsFailInsteadOfFallingBack(t *testing.T) {
	ctx := context.Background()
	for key, bad := range map[string]string{keyTZ: "Mars/Base", keyWindow: "xx", keyQuiet: "2200", keyGlobalOff: "{"} {
		f := newFixture(t)
		if err := f.st.SetProactiveSetting(ctx, key, bad); err != nil {
			t.Fatal(err)
		}
		if err := f.svc.Enable(ctx, "gmail.morning"); err == nil {
			t.Errorf("%s=%q: Enable succeeded on a corrupt setting", key, bad)
		}
		if f.taskCount() != 0 {
			t.Errorf("%s=%q: a task was created", key, bad)
		}
		if _, err := f.svc.Status(ctx, "gmail.morning"); err == nil && key == keyGlobalOff {
			t.Errorf("Status hid a corrupt pause set")
		}
	}
}

func TestCorruptPresetSettingsFail(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	row, _, _ := f.st.GetProactivePreset(ctx, "gmail.morning")
	row.Settings = "{not json"
	if err := f.st.PutProactivePreset(ctx, row); err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.Status(ctx, "gmail.morning"); err == nil {
		t.Fatal("corrupt preset settings hidden")
	}
}

// --- tests listed under item 12 ---

func TestSetTimezoneRollsBackWhenALaterPresetFails(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.enable("gmail.morning")
	f.enable("cal.weekly")
	row, _, _ := f.st.GetProactivePreset(ctx, "cal.weekly") // processed after gmail.morning
	row.Settings = "{not json"
	if err := f.st.PutProactivePreset(ctx, row); err != nil {
		t.Fatal(err)
	}
	before := f.task("gmail.morning")
	if _, err := f.svc.SetTimezone(ctx, "Asia/Tokyo"); err == nil {
		t.Fatal("expected the mid-loop failure to surface")
	}
	if f.task("gmail.morning") != before {
		t.Fatal("earlier preset kept the new zone after a failed change")
	}
	if tz, _ := f.svc.Timezone(ctx); tz != DefaultTimezone {
		t.Fatalf("zone stored despite failure: %s", tz)
	}
}

func TestConcurrentSetTimezoneAndEnable(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 6; i++ {
		wg.Add(2)
		tz := []string{"UTC", "Asia/Tokyo", "America/New_York"}[i%3]
		go func() {
			defer wg.Done()
			if _, err := f.svc.SetTimezone(ctx, tz); err != nil {
				t.Error(err)
			}
		}()
		go func(id string) {
			defer wg.Done()
			if err := f.svc.Enable(ctx, id); err != nil {
				t.Error(err)
			}
		}([]string{"gmail.morning", "cal.agenda", "gmail.urgent"}[i%3])
	}
	wg.Wait()
	zone := f.tz()
	for _, id := range []string{"gmail.morning", "cal.agenda", "gmail.urgent"} {
		if tk := f.task(id); !strings.HasPrefix(tk.ScheduleValue, "CRON_TZ="+zone+" ") || f.status(id).Modified {
			t.Errorf("%s ends inconsistent with zone %s: %+v", id, zone, tk)
		}
	}
}

// A polling window and a quiet edge on the DST transition days of Belgrade.
func TestPollingScheduleOnDSTTransitionDays(t *testing.T) {
	loc, _ := time.LoadLocation("Europe/Belgrade")
	q, _ := ParseQuiet("0200", "0300")
	sched, err := Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "60m"}, Env{TZ: "Europe/Belgrade", Window: Window{0, 6}, Quiet: &q})
	if err != nil || sched != "CRON_TZ=Europe/Belgrade 0 0-1,3-5 * * 1-5" {
		t.Fatalf("sched = %q err=%v", sched, err)
	}
	// Monday 2027-03-29 is after the spring-forward Sunday; use the weekday
	// either side of each transition: Fri 2026-10-23 .. Mon 2026-10-26 and
	// Fri 2027-03-26 .. Mon 2027-03-29.
	for _, start := range []time.Time{time.Date(2026, 10, 23, 0, 0, 0, 0, loc), time.Date(2027, 3, 26, 0, 0, 0, 0, loc)} {
		now := start.Add(-time.Minute)
		seen := map[int]int{}
		for i := 0; i < 40; i++ {
			ms, err := scheduler.FirstRunAt("cron", sched, now)
			if err != nil {
				t.Fatal(err)
			}
			next := time.UnixMilli(ms).In(loc)
			if h := next.Hour(); h == 2 || h > 5 || next.Minute() != 0 {
				t.Fatalf("run at %s is outside the allowed hours", next)
			}
			if wd := next.Weekday(); wd == time.Saturday || wd == time.Sunday {
				t.Fatalf("run on %s", next)
			}
			seen[next.YearDay()*100+next.Hour()]++
			now = next
		}
		for k, n := range seen {
			if n > 1 {
				t.Fatalf("slot %d fired %d times", k, n)
			}
		}
	}
	// A working window across the change keeps its local hours.
	win, _ := Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "60m"}, env())
	now := time.Date(2026, 10, 26, 8, 0, 0, 0, loc) // Monday after the fall-back
	ms, _ := scheduler.FirstRunAt("cron", win, now)
	if got := time.UnixMilli(ms).In(loc); got.Hour() != 9 || got.Day() != 26 {
		t.Fatalf("first poll after fall-back = %s", got)
	}
}

// An IPC update that changes only the schedule type leaves a cron-looking value
// behind; it is an owner edit (hash covers the type) and must keep its type.
func TestTypeOnlyEditIsDetectedAndKeepsItsType(t *testing.T) {
	f := newFixture(t)
	f.enable("gmail.morning")
	f.setTask("gmail.morning", func(tk *store.TaskRecord) { tk.ScheduleType = "interval" })
	if !f.status("gmail.morning").Modified {
		t.Fatal("type-only edit not detected")
	}
	if _, err := f.svc.SetTimezone(context.Background(), "UTC"); err != nil {
		t.Fatal(err)
	}
	if got := f.task("gmail.morning"); got.ScheduleType != "interval" || got.ScheduleValue != "CRON_TZ=Europe/Belgrade 0 8 * * *" {
		t.Fatalf("edited task rewritten: %+v", got)
	}
}
