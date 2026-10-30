package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/proactive"
	"github.com/Coriol-is/gopod/internal/store"
)

type proactiveFx struct {
	t      *testing.T
	st     *store.Store
	svc    *proactive.Service
	r      *Router
	status ConnectorStatus
}

func newProactiveFx(t *testing.T) *proactiveFx {
	t.Helper()
	st := testStore(t)
	now := func() time.Time { return time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC) }
	svc, err := proactive.New(proactive.Config{Store: st, Folder: "owner", ChatJID: "tg:100", Now: now})
	if err != nil {
		t.Fatal(err)
	}
	fx := &proactiveFx{t: t, st: st, svc: svc, r: New(silentLog()), status: ConnectorUnknown}
	RegisterBuiltins(fx.r)
	RegisterProactiveCommand(fx.r, ProactiveDeps{
		Service:   func(context.Context) (*proactive.Service, error) { return svc, nil },
		Connector: func(context.Context, string) ConnectorStatus { return fx.status },
		Now:       now,
	})
	return fx
}

func ownerPrivate() Caller {
	return Caller{Source: SourceTelegram, ChatKind: "private", ChatID: 100, UserID: 100, IsOwner: true}
}

func (fx *proactiveFx) run(args ...string) Response {
	fx.t.Helper()
	resp, err := fx.r.Dispatch(t0(fx.t), Command{Name: "proactive", Args: args, Caller: ownerPrivate()})
	if err != nil {
		fx.t.Fatalf("/proactive %v: %v", args, err)
	}
	return resp
}

func t0(t *testing.T) context.Context { return t.Context() }

// confirmed re-dispatches a command as the Yes button of a confirmation does.
func (fx *proactiveFx) confirmed(args ...string) Response {
	fx.t.Helper()
	resp, err := fx.r.Dispatch(t0(fx.t), Command{Name: "proactive", Args: args, Caller: ownerPrivate(), Confirmed: true})
	if err != nil {
		fx.t.Fatal(err)
	}
	return resp
}

// press dispatches the button whose label contains label, like a frontend does.
func (fx *proactiveFx) press(resp Response, label string) Response {
	fx.t.Helper()
	b := findButton(fx.t, resp, label)
	out, err := fx.r.Dispatch(t0(fx.t), Command{Name: b.Command, Args: b.Args, Caller: ownerPrivate(), Confirmed: b.Confirmed})
	if err != nil {
		fx.t.Fatal(err)
	}
	return out
}

func findButton(t *testing.T, resp Response, label string) Button {
	t.Helper()
	for _, row := range resp.Buttons {
		for _, b := range row {
			if strings.Contains(b.Label, label) {
				return b
			}
		}
	}
	t.Fatalf("no button %q in %+v", label, resp.Buttons)
	return Button{}
}

func labels(resp Response) []string {
	var out []string
	for _, row := range resp.Buttons {
		for _, b := range row {
			out = append(out, b.Label)
		}
	}
	return out
}

func (fx *proactiveFx) task(id string) store.TaskRecord {
	fx.t.Helper()
	tk, err := fx.st.GetTask(t0(fx.t), proactive.TaskID(id))
	if err != nil {
		fx.t.Fatalf("task %s: %v", id, err)
	}
	return tk
}

func (fx *proactiveFx) hasTask(id string) bool {
	_, err := fx.st.GetTask(t0(fx.t), proactive.TaskID(id))
	return err == nil
}

func (fx *proactiveFx) enable(args ...string) {
	fx.t.Helper()
	if resp := fx.confirmed(args...); resp.Code != 0 {
		fx.t.Fatalf("enable %v: %+v", args, resp)
	}
}

func TestProactiveIsRefusedByTheRouterOutsideTheOwnerPrivateChat(t *testing.T) {
	fx := newProactiveFx(t)
	callers := map[string]Caller{
		"owner in a group": {Source: SourceTelegram, ChatKind: "supergroup", ChatID: -1001, UserID: 100, IsGroupOwner: true},
		"member in group":  {Source: SourceTelegram, ChatKind: "supergroup", ChatID: -1001, UserID: 5},
		"stranger private": {Source: SourceTelegram, ChatKind: "private", ChatID: 5, UserID: 5},
	}
	for name, c := range callers {
		for _, args := range [][]string{nil, {"gmail", "urgent", "on"}, {"off"}, {"tz", "Asia/Tokyo"}} {
			resp, err := fx.r.Dispatch(t.Context(), Command{Name: "proactive", Args: args, Caller: c, Confirmed: true})
			if !errors.Is(err, ErrNotAuthorized) || strings.Contains(strings.ToLower(resp.Text), "gmail") {
				t.Errorf("%s %v: err=%v resp=%+v", name, args, err, resp)
			}
		}
	}
	if fx.hasTask("gmail.urgent") {
		t.Fatal("a refused press created a task")
	}
}

func TestProactiveHelpEntryIsOwnerOnly(t *testing.T) {
	fx := newProactiveFx(t)
	owner, _ := fx.r.Dispatch(t.Context(), Command{Name: "help", Caller: ownerPrivate()})
	if !strings.Contains(owner.Text, "/proactive") {
		t.Fatalf("owner help: %s", owner.Text)
	}
	other, _ := fx.r.Dispatch(t.Context(), Command{Name: "help", Caller: Caller{Source: SourceTelegram, ChatID: 5}})
	if strings.Contains(other.Text, "/proactive") {
		t.Fatalf("non-owner help lists /proactive")
	}
}

func TestProactiveOverviewShowsSettingsAndConnectors(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run()
	for _, want := range []string{"Proactive actions", "Europe/Belgrade", "09-19", "quiet off", "Gmail", "Calendar", "0 of 3 on"} {
		if !strings.Contains(resp.Text, want) {
			t.Errorf("overview lacks %q:\n%s", want, resp.Text)
		}
	}
	for _, l := range []string{"Gmail", "Calendar", "Timezone", "Hours", "Quiet", "Pause all"} {
		findButton(t, resp, l)
	}
	if resp.Code != 0 {
		t.Fatal(resp.Code)
	}
}

func TestProactiveOverviewCountsEnabledAndOffersResumeWhenPaused(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on")
	if !strings.Contains(fx.run().Text, "Gmail: 1 of 3 on") {
		t.Fatalf("count: %s", fx.run().Text)
	}
	paused := fx.press(fx.run(), "Pause all")
	if !strings.Contains(paused.Text, "paused") {
		t.Fatalf("pause reply: %s", paused.Text)
	}
	if fx.task("gmail.morning").Status != "paused" {
		t.Fatal("task not paused")
	}
	findButton(t, paused, "Resume all")
	resumed := fx.press(paused, "Resume all")
	if fx.task("gmail.morning").Status != "active" || !strings.Contains(resumed.Text, "Resumed") {
		t.Fatalf("resume: %+v", resumed)
	}
}

func TestProactiveTextFormsForGlobalOffAndOn(t *testing.T) {
	fx := newProactiveFx(t)
	fx.run("off")
	resp := fx.run("gmail", "morning", "on")
	if !strings.Contains(resp.Text, "paused") || resp.Code != 1 || fx.hasTask("gmail.morning") {
		t.Fatalf("enable during pause must be refused with a clear message: %+v", resp)
	}
	fx.run("on")
	if ok := fx.run("gmail", "morning", "on"); ok.Code != CodeConfirm {
		t.Fatalf("after resume the enable asks to confirm: %+v", ok)
	}
}

func TestProactiveConnectorScreenListsItsPresets(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run("gmail")
	for _, want := range []string{"morning summary", "important mail check", "evening wrap-up", "daily 08:00", "every 60 min", "10 runs/day"} {
		if !strings.Contains(resp.Text, want) {
			t.Errorf("gmail screen lacks %q:\n%s", want, resp.Text)
		}
	}
	b := findButton(t, resp, "Urgent")
	if b.Command != "proactive" || strings.Join(b.Args, " ") != "gmail urgent" {
		t.Fatalf("button %+v", b)
	}
	cal := fx.run("calendar")
	for _, want := range []string{"today's agenda", "meeting reminders", "week ahead"} {
		if !strings.Contains(cal.Text, want) {
			t.Errorf("calendar screen lacks %q", want)
		}
	}
	if back := findButton(t, resp, "Back"); len(back.Args) != 0 {
		t.Fatalf("back goes to the overview: %+v", back)
	}
}

func TestProactivePresetScreenMarksTheCurrentChoices(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run("gmail", "urgent")
	if !strings.Contains(resp.Text, "important mail check") || !strings.Contains(resp.Text, "off") {
		t.Fatalf("screen: %s", resp.Text)
	}
	if !strings.HasPrefix(findButton(t, resp, "60m").Label, "✓ ") || strings.HasPrefix(findButton(t, resp, "30m").Label, "✓") {
		t.Fatalf("frequency marks: %v", labels(resp))
	}
	if !strings.HasPrefix(findButton(t, resp, "Off").Label, "✓ ") {
		t.Fatalf("state marks: %v", labels(resp))
	}
	fx.enable("gmail", "urgent", "on")
	on := fx.run("gmail", "urgent")
	if !strings.HasPrefix(findButton(t, on, "On").Label, "✓ ") {
		t.Fatalf("on marked: %v", labels(on))
	}
}

func TestProactiveEnableOnUnknownStatusAsksAndYesEnables(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run("gmail", "urgent", "on")
	if resp.Code != CodeConfirm || fx.hasTask("gmail.urgent") {
		t.Fatalf("expected a confirmation and no task: %+v", resp)
	}
	if !strings.Contains(resp.Text, "not been checked") {
		t.Fatalf("question must say the connector is unchecked: %s", resp.Text)
	}
	yes := resp.Buttons[0][0]
	if !yes.Confirmed || yes.Command != "proactive" {
		t.Fatalf("yes %+v", yes)
	}
	done := fx.press(resp, "Yes")
	if done.Code != 0 || fx.task("gmail.urgent").Status != "active" {
		t.Fatalf("after yes: %+v", done)
	}
	if !strings.Contains(done.Text, "on") {
		t.Fatalf("result screen: %s", done.Text)
	}
}

func TestProactiveEnableOnReadyConnectorNeedsNoQuestion(t *testing.T) {
	fx := newProactiveFx(t)
	fx.status = ConnectorReady
	if resp := fx.run("gmail", "urgent", "on"); resp.Code != 0 || !fx.hasTask("gmail.urgent") {
		t.Fatalf("%+v", resp)
	}
}

func TestProactiveEnableRefusedWhenConnectorIsNotConnected(t *testing.T) {
	fx := newProactiveFx(t)
	fx.status = ConnectorNotConnected
	resp := fx.run("gmail", "urgent", "on")
	if resp.Code != 1 || fx.hasTask("gmail.urgent") || !strings.Contains(resp.Text, "not connected") {
		t.Fatalf("%+v", resp)
	}
	if fx.confirmed("gmail", "urgent", "on").Code != 1 || fx.hasTask("gmail.urgent") {
		t.Fatal("a confirmation must not override a known-bad connector")
	}
}

func TestProactiveDisablePausesTheTask(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on")
	resp := fx.press(fx.run("gmail", "morning"), "Off")
	if fx.task("gmail.morning").Status != "paused" || resp.Code != 0 {
		t.Fatalf("%+v", resp)
	}
	if fx.run("gmail", "morning", "off").Code != 0 { // idempotent
		t.Fatal("second off failed")
	}
}

func TestProactiveFrequencyPickerChangesTheCron(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "urgent", "on")
	resp := fx.press(fx.run("gmail", "urgent"), "30m")
	if got := fx.task("gmail.urgent").ScheduleValue; got != "CRON_TZ=Europe/Belgrade */30 9-18 * * 1-5" {
		t.Fatalf("cron %q", got)
	}
	if !strings.HasPrefix(findButton(t, resp, "30m").Label, "✓ ") || !strings.Contains(resp.Text, "20 runs/day") {
		t.Fatalf("screen not refreshed: %s %v", resp.Text, labels(resp))
	}
	if fx.run("gmail", "urgent", "freq", "60m").Code != 0 {
		t.Fatal("text form")
	}
}

func TestProactiveFrequencyOnAnOffPresetIsStoredForLater(t *testing.T) {
	fx := newProactiveFx(t)
	fx.run("gmail", "urgent", "freq", "15m")
	if fx.hasTask("gmail.urgent") {
		t.Fatal("configuring must not enable")
	}
	fx.enable("gmail", "urgent", "on")
	if !strings.Contains(fx.task("gmail.urgent").ScheduleValue, "*/15") {
		t.Fatal("stored frequency lost")
	}
}

func TestProactiveStaleOrInvalidValuesAreRevalidated(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on")
	before := fx.task("gmail.morning").ScheduleValue
	for _, args := range [][]string{
		{"gmail", "morning", "freq", "30m"}, // an interval is not offered for morning
		{"gmail", "morning", "freq", ""},
		{"gmail", "morning", "time", "0500"},
		{"gmail", "morning", "time", "8"},
		{"gmail", "morning", "time", "2500"},
		{"gmail", "morning", "lead", "10"},
		{"gmail", "urgent", "time", "0800"},
		{"gmail", "morning", "freq", "daily", "extra"},
		{"gmail", "nope"},
		{"nope"},
		{"gmail", "morning", "explode"},
	} {
		resp := fx.run(args...)
		if resp.Code != 1 || resp.Text == "" {
			t.Errorf("%v accepted: %+v", args, resp)
		}
	}
	if fx.task("gmail.morning").ScheduleValue != before {
		t.Fatal("an invalid press changed the schedule")
	}
	bad := fx.run("gmail", "morning", "freq", "30m")
	if !strings.Contains(bad.Text, "daily") || len(bad.Buttons) == 0 {
		t.Fatalf("a rejected press shows the allowed values and a fresh screen: %+v", bad)
	}
}

func TestProactiveTimePickerHidesQuietHoursAndSetsTheTime(t *testing.T) {
	fx := newProactiveFx(t)
	fx.run("quiet", "2200-0730")
	pick := fx.run("gmail", "morning", "time")
	got := strings.Join(labels(pick), " ")
	if strings.Contains(got, "0600") || strings.Contains(got, "0700") || !strings.Contains(got, "0800") {
		t.Fatalf("picker hours: %s", got)
	}
	fx.enable("gmail", "morning", "on")
	fx.press(fx.run("gmail", "morning", "time"), "0900")
	if got := fx.task("gmail.morning").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 0 9 * * *" {
		t.Fatalf("cron %q", got)
	}
	if !strings.HasPrefix(findButton(t, fx.run("gmail", "morning", "time"), "0900").Label, "✓ ") {
		t.Fatal("current time not marked")
	}
	if findButton(t, fx.run("gmail", "morning"), "Time").Command != "proactive" {
		t.Fatal("preset screen links to the time picker")
	}
}

func TestProactiveLeadForReminderOnly(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("calendar", "reminder", "on")
	resp := fx.press(fx.run("calendar", "reminder"), "30")
	if !strings.Contains(fx.task("cal.reminder").Prompt, "30 minutes") || !strings.Contains(resp.Text, "30") {
		t.Fatalf("lead not applied: %s", fx.task("cal.reminder").Prompt)
	}
	if fx.run("calendar", "reminder", "lead", "20").Code != 1 {
		t.Fatal("lead 20 is not offered")
	}
}

func TestProactiveErrorsAreMappedToClearReplies(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on")
	// ErrUserModified: edit the task outside /proactive.
	tk := fx.task("gmail.morning")
	tk.Prompt = "my own prompt"
	if err := fx.st.UpdateTaskDefinition(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	resp := fx.run("gmail", "morning", "freq", "weekdays")
	if resp.Code != 1 || !strings.Contains(resp.Text, "/tasks") || !strings.Contains(resp.Text, "Reset to template") {
		t.Fatalf("user modified: %+v", resp)
	}
	// The screen offers the reset, behind a confirmation.
	screen := fx.run("gmail", "morning")
	if !strings.Contains(screen.Text, "edited in /tasks: yes") {
		t.Fatalf("screen: %s", screen.Text)
	}
	ask := fx.press(screen, "Reset to template")
	if ask.Code != CodeConfirm || fx.task("gmail.morning").Prompt != "my own prompt" {
		t.Fatalf("reset must ask first: %+v", ask)
	}
	fx.press(ask, "Yes")
	if fx.task("gmail.morning").Prompt == "my own prompt" {
		t.Fatal("reset did not restore the template")
	}
	// ErrInsideQuiet: a fixed time inside quiet hours.
	fx.run("quiet", "0000-0700")
	in := fx.run("gmail", "morning", "time", "0600")
	if in.Code != 1 || !strings.Contains(in.Text, "quiet hours") {
		t.Fatalf("inside quiet: %+v", in)
	}
	// ErrNoRunHours: quiet hours cover the whole window.
	fx.run("quiet", "off")
	fx.run("hours", "0900-1000")
	nr := fx.run("quiet", "0800-1100")
	if !strings.Contains(nr.Text, "Paused") || !strings.Contains(nr.Text, "morning summary") {
		t.Fatalf("quiet over a preset should report it paused: %s", nr.Text)
	}
	got := fx.confirmed("gmail", "urgent", "on")
	if got.Code != 1 || !strings.Contains(got.Text, "no run hours") {
		t.Fatalf("no run hours: %+v", got)
	}
}

func TestProactiveTaskConflictIsExplained(t *testing.T) {
	fx := newProactiveFx(t)
	if _, err := fx.st.CreateTask(t.Context(), store.TaskRecord{
		ID: proactive.TaskID("gmail.urgent"), ChatFolder: "someone-else", ChatJID: "tg:-5", Prompt: "x",
		ScheduleType: "cron", ScheduleValue: "0 9 * * *", NextRun: time.Now().Add(time.Hour).UnixMilli(), Status: "active",
	}); err != nil {
		t.Fatal(err)
	}
	resp := fx.confirmed("gmail", "urgent", "on")
	if resp.Code != 1 || !strings.Contains(resp.Text, "another") {
		t.Fatalf("conflict: %+v", resp)
	}
}

func TestProactiveTimezonePickerMarksTheCurrentZoneAndPressSetsIt(t *testing.T) {
	fx := newProactiveFx(t)
	resp := fx.run("tz")
	zones := []string{"Europe/Belgrade", "Europe/Moscow", "Europe/London", "Europe/Berlin", "America/New_York", "America/Los_Angeles", "Asia/Dubai", "Asia/Tokyo"}
	for _, z := range zones {
		findButton(t, resp, z)
	}
	if b := findButton(t, resp, "Belgrade"); b.Label != "✓ Europe/Belgrade" || b.Style != StylePrimary {
		t.Fatalf("current not marked: %+v", b)
	}
	if !strings.Contains(resp.Text, "Europe/Belgrade") || !strings.Contains(resp.Text, "/proactive tz") {
		t.Fatalf("text: %s", resp.Text)
	}
	for _, row := range resp.Buttons {
		for _, b := range row {
			if strings.Contains(b.Label, "Tokyo") && (b.Command != "proactive" || strings.Join(b.Args, "|") != "tz|Asia/Tokyo") {
				t.Fatalf("tokyo button %+v", b)
			}
		}
	}
	fx.enable("gmail", "morning", "on")
	done := fx.press(resp, "Tokyo")
	if got := fx.task("gmail.morning").ScheduleValue; got != "CRON_TZ=Asia/Tokyo 0 8 * * *" {
		t.Fatalf("cron %q", got)
	}
	if !strings.Contains(done.Text, "Timezone set to") || !strings.Contains(done.Text, "morning summary") {
		t.Fatalf("reply: %s", done.Text)
	}
	if tz, _ := fx.svc.Timezone(t.Context()); tz != "Asia/Tokyo" {
		t.Fatal(tz)
	}
	if !strings.HasPrefix(findButton(t, fx.run("tz"), "Tokyo").Label, "✓ ") {
		t.Fatal("picker does not follow the stored zone")
	}
}

func TestProactiveTimezoneTypedFormValidatesLikeThePicker(t *testing.T) {
	fx := newProactiveFx(t)
	for _, bad := range []string{"Mars/Base", "Europe/Belgrad", "Local", "", strings.Repeat("a", 65)} {
		resp := fx.run("tz", bad)
		if resp.Code != 1 || !strings.Contains(resp.Text, "Unknown zone") || len(resp.Buttons) == 0 {
			t.Errorf("%q accepted or no picker: %+v", bad, resp)
		}
	}
	if resp := fx.run("tz", "Europe/Belgrade", "extra"); resp.Code != 1 {
		t.Errorf("two words: %+v", resp)
	}
	if tz, _ := fx.svc.Timezone(t.Context()); tz != "Europe/Belgrade" {
		t.Fatalf("an invalid zone changed the setting: %s", tz)
	}
	for _, ok := range []string{"UTC", "Asia/Tbilisi"} {
		if resp := fx.run("tz", ok); resp.Code != 0 {
			t.Errorf("%s: %+v", ok, resp)
		}
	}
	if tz, _ := fx.svc.Timezone(t.Context()); tz != "Asia/Tbilisi" {
		t.Fatal(tz)
	}
}

func TestProactiveTimezoneChangeNamesEditedPresetsItKept(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on")
	fx.enable("calendar", "agenda", "on")
	tk := fx.task("gmail.morning")
	tk.Prompt = "edited"
	if err := fx.st.UpdateTaskDefinition(t.Context(), tk); err != nil {
		t.Fatal(err)
	}
	resp := fx.run("tz", "Europe/London")
	if !strings.Contains(resp.Text, "morning summary") || !strings.Contains(strings.ToLower(resp.Text), "not changed") && !strings.Contains(strings.ToLower(resp.Text), "edited") {
		t.Fatalf("edited preset should be named: %s", resp.Text)
	}
	if !strings.Contains(fx.task("cal.agenda").ScheduleValue, "CRON_TZ=Europe/London") {
		t.Fatal("agenda not rescheduled")
	}
}

func TestProactiveHoursAndQuietPickersAndTextForms(t *testing.T) {
	fx := newProactiveFx(t)
	h := fx.run("hours")
	if !strings.HasPrefix(findButton(t, h, "0900-1900").Label, "✓ ") {
		t.Fatalf("hours picker: %v", labels(h))
	}
	fx.enable("gmail", "urgent", "on")
	fx.press(h, "0800-1800")
	if w, _ := fx.svc.Window(t.Context()); w.StartHour != 8 || w.EndHour != 18 {
		t.Fatalf("window %+v", w)
	}
	if got := fx.task("gmail.urgent").ScheduleValue; got != "CRON_TZ=Europe/Belgrade 0 8-17 * * 1-5" {
		t.Fatalf("cron %q", got)
	}
	if fx.run("hours", "10-16").Code != 0 {
		t.Fatal("two-digit form")
	}
	for _, bad := range []string{"1900-0900", "0930-1800", "abc", "0900", "0900-2500"} {
		if resp := fx.run("hours", bad); resp.Code != 1 {
			t.Errorf("hours %q accepted", bad)
		}
	}
	q := fx.run("quiet")
	if !strings.HasPrefix(findButton(t, q, "off").Label, "✓ ") {
		t.Fatalf("quiet picker: %v", labels(q))
	}
	fx.press(q, "2200-0800")
	if qq, _ := fx.svc.Quiet(t.Context()); qq == nil || qq.StartMin != 22*60 || qq.EndMin != 8*60 {
		t.Fatalf("quiet %+v", qq)
	}
	if !strings.Contains(fx.run().Text, "quiet 22:00-08:00") {
		t.Fatal("overview does not show quiet hours")
	}
	fx.run("quiet", "off")
	if qq, _ := fx.svc.Quiet(t.Context()); qq != nil {
		t.Fatal("quiet not cleared")
	}
	for _, bad := range []string{"2200", "2200-2200", "2600-0800", "x-y"} {
		if resp := fx.run("quiet", bad); resp.Code != 1 {
			t.Errorf("quiet %q accepted", bad)
		}
	}
}

func TestProactiveQuietThatPausesAPresetSaysSo(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("gmail", "morning", "on") // 08:00
	resp := fx.run("quiet", "0700-0900")
	if !strings.Contains(resp.Text, "morning summary") || !strings.Contains(resp.Text, "quiet hours") {
		t.Fatalf("reply: %s", resp.Text)
	}
	if fx.task("gmail.morning").Status != "paused" {
		t.Fatal("not paused")
	}
	if !strings.Contains(fx.run("gmail", "morning").Text, "inside quiet hours") {
		t.Fatal("preset screen must give the pause reason")
	}
}

func TestProactiveNotYetBuiltSubcommandsSayTheyAreNotAvailable(t *testing.T) {
	fx := newProactiveFx(t)
	for _, sub := range []string{"check", "guide", "offer"} {
		resp := fx.run(sub, "gmail")
		if resp.Code != 1 || !strings.Contains(resp.Text, "not available") {
			t.Errorf("%s: %+v", sub, resp)
		}
	}
}

func TestProactiveNoButtonArgContainsAColonAndEveryButtonRuns(t *testing.T) {
	fx := newProactiveFx(t)
	fx.enable("calendar", "reminder", "on")
	screens := []Response{fx.run(), fx.run("gmail"), fx.run("calendar"), fx.run("gmail", "urgent"), fx.run("gmail", "morning"),
		fx.run("calendar", "reminder"), fx.run("gmail", "morning", "time"), fx.run("tz"), fx.run("hours"), fx.run("quiet")}
	for _, s := range screens {
		for _, row := range s.Buttons {
			for _, b := range row {
				for _, a := range b.Args {
					if strings.Contains(a, ":") {
						t.Errorf("button %q arg %q has a colon", b.Label, a)
					}
				}
				if b.Command == "" || b.Confirmed {
					continue
				}
				resp, err := fx.r.Dispatch(t.Context(), Command{Name: b.Command, Args: b.Args, Caller: ownerPrivate()})
				if err != nil || resp.Text == "" {
					t.Errorf("button %q (%v) does not run: %+v %v", b.Label, b.Args, resp, err)
				}
			}
		}
	}
}
