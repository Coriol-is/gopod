package control

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/proactive"
)

// tzChoices are the zones the picker offers; any IANA name works as text.
var tzChoices = []string{
	"Europe/Belgrade", "Europe/Moscow",
	"Europe/London", "Europe/Berlin",
	"America/New_York", "America/Los_Angeles",
	"Asia/Dubai", "Asia/Tokyo",
}

func (h proactiveHandler) timezone(ctx context.Context, svc *proactive.Service, args []string) (Response, error) {
	switch len(args) {
	case 0:
		return h.tzPicker(ctx, svc, "", 0)
	case 1:
	default:
		return h.tzPicker(ctx, svc, "Usage: /proactive tz [<IANA zone>]", 1)
	}
	if err := proactive.ValidateTimezone(args[0]); err != nil {
		return h.tzPicker(ctx, svc, fmt.Sprintf("Unknown zone %q. Use an IANA name such as Europe/Belgrade.", plain(args[0])), 1)
	}
	rep, err := svc.SetTimezone(ctx, args[0])
	if err != nil {
		return fail(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "Timezone set to `%s`.", args[0])
	loc, _ := time.LoadLocation(args[0])
	var next []string
	for _, id := range rep.Rescheduled {
		st, err := svc.Status(ctx, id)
		if err != nil {
			return fail(err)
		}
		line := st.Preset.Title
		if st.NextRun > 0 && loc != nil {
			line += " next " + time.UnixMilli(st.NextRun).In(loc).Format("Mon 15:04")
		}
		next = append(next, line)
	}
	if len(next) > 0 {
		sb.WriteString(" Rescheduled: " + strings.Join(next, ", ") + ".")
	}
	if len(rep.Kept) > 0 {
		sb.WriteString(" Not changed (edited in /tasks): " + titles(rep.Kept) + ".")
	}
	if len(rep.Paused) > 0 {
		sb.WriteString(" Paused (inside quiet hours): " + titles(rep.Paused) + ".")
	}
	return Response{Text: sb.String(), Buttons: [][]Button{{backToOverview}}}, nil
}

func (h proactiveHandler) tzPicker(ctx context.Context, svc *proactive.Service, notice string, code int) (Response, error) {
	cur, err := svc.Timezone(ctx)
	if err != nil {
		return fail(err)
	}
	offset := ""
	if loc, lerr := time.LoadLocation(cur); lerr == nil {
		now := h.deps.Now().In(loc)
		name, off := now.Zone()
		offset = fmt.Sprintf(" (UTC%s%02d:%02d, %s)", map[bool]string{true: "+", false: "-"}[off >= 0], abs(off)/3600, abs(off)%3600/60, name)
	}
	text := fmt.Sprintf("**Timezone**\nNow: `%s`%s\nPresets run in this zone.\nAnother zone: /proactive tz <IANA name>, for example /proactive tz Asia/Tbilisi", cur, offset)
	var rows [][]Button
	var row []Button
	for _, z := range tzChoices {
		row = append(row, presetButton(z, mark(z == cur), "tz", z))
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []Button{backToOverview})
	return withNotice(Response{Text: text, Buttons: rows}, notice, code), nil
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// hourChoices and quietChoices are the pickers' presets; any range works as text.
var (
	hourChoices  = []string{"0900-1900", "0800-1800", "0800-2000", "1000-1800"}
	quietChoices = []string{"off", "2200-0800", "2300-0700", "2300-0800", "0000-0700"}
)

// splitRange splits "0900-1900" or "09-19" into two HHMM strings.
func splitRange(s string) (from, to string, ok bool) {
	a, b, found := strings.Cut(s, "-")
	if !found {
		return "", "", false
	}
	norm := func(v string) string {
		if len(v) == 2 {
			return v + "00"
		}
		return v
	}
	return norm(a), norm(b), true
}

// hourOf reads a whole hour from HHMM ("2400" is the end of the day).
func hourOf(hhmm string) (int, bool) {
	if len(hhmm) != 4 || hhmm[2:] != "00" {
		return 0, false
	}
	n, ok := atoi(strings.TrimPrefix(hhmm[:2], "0"))
	if hhmm[:2] == "00" {
		return 0, true
	}
	return n, ok
}

func (h proactiveHandler) hours(ctx context.Context, svc *proactive.Service, args []string) (Response, error) {
	usage := "Use whole hours, start before end, for example /proactive hours 0900-1900."
	if len(args) == 0 {
		return h.hoursPicker(ctx, svc, "", 0)
	}
	if len(args) != 1 {
		return h.hoursPicker(ctx, svc, usage, 1)
	}
	from, to, ok := splitRange(args[0])
	a, ok1 := hourOf(from)
	b, ok2 := hourOf(to)
	if !ok || !ok1 || !ok2 {
		return h.hoursPicker(ctx, svc, fmt.Sprintf("Invalid window %q. %s", plain(args[0]), usage), 1)
	}
	if err := svc.SetWindow(ctx, proactive.Window{StartHour: a, EndHour: b}); err != nil {
		return h.hoursPicker(ctx, svc, fmt.Sprintf("Invalid window %q. %s", plain(args[0]), usage), 1)
	}
	return h.hoursPicker(ctx, svc, fmt.Sprintf("Working window set to %02d-%02d.%s", a, b, h.pausedByQuiet(ctx, svc)), 0)
}

func (h proactiveHandler) hoursPicker(ctx context.Context, svc *proactive.Service, notice string, code int) (Response, error) {
	w, err := svc.Window(ctx)
	if err != nil {
		return fail(err)
	}
	cur := fmt.Sprintf("%02d00-%02d00", w.StartHour, w.EndHour)
	text := fmt.Sprintf("**Working window**\nNow: weekdays %s. Only polling presets use it (important mail check, meeting reminders). End hour is exclusive.\nOther ranges: /proactive hours HHMM-HHMM, whole hours.", windowText(w))
	return withNotice(Response{Text: text, Buttons: h.rangeRows("hours", hourChoices, cur)}, notice, code), nil
}

func (h proactiveHandler) rangeRows(cmd string, choices []string, cur string) [][]Button {
	var rows [][]Button
	var row []Button
	for _, c := range choices {
		row = append(row, presetButton(c, mark(c == cur), cmd, c))
		if len(row) == 2 {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	return append(rows, []Button{backToOverview})
}

func (h proactiveHandler) quiet(ctx context.Context, svc *proactive.Service, args []string) (Response, error) {
	usage := "Use /proactive quiet 2200-0800, or /proactive quiet off."
	if len(args) == 0 {
		return h.quietPicker(ctx, svc, "", 0)
	}
	if len(args) != 1 {
		return h.quietPicker(ctx, svc, usage, 1)
	}
	if args[0] == "off" {
		if err := svc.SetQuiet(ctx, nil); err != nil {
			return fail(err)
		}
		return h.quietPicker(ctx, svc, "Quiet hours are off.", 0)
	}
	from, to, ok := splitRange(args[0])
	if !ok {
		return h.quietPicker(ctx, svc, fmt.Sprintf("Invalid quiet window %q. %s", plain(args[0]), usage), 1)
	}
	q, err := proactive.ParseQuiet(from, to)
	if err != nil {
		return h.quietPicker(ctx, svc, fmt.Sprintf("Invalid quiet window %q. %s", plain(args[0]), usage), 1)
	}
	if err = svc.SetQuiet(ctx, &q); err != nil {
		return fail(err)
	}
	return h.quietPicker(ctx, svc, fmt.Sprintf("Quiet hours set to %s.%s", quietText(&q), h.pausedByQuiet(ctx, svc)), 0)
}

func (h proactiveHandler) quietPicker(ctx context.Context, svc *proactive.Service, notice string, code int) (Response, error) {
	q, err := svc.Quiet(ctx)
	if err != nil {
		return fail(err)
	}
	cur := "off"
	if q != nil {
		cur = fmt.Sprintf("%02d%02d-%02d%02d", q.StartMin/60, q.StartMin%60, q.EndMin/60, q.EndMin%60)
	}
	text := fmt.Sprintf("**Quiet hours**\nNow: %s. Presets do not run inside quiet hours (a run is skipped, not delayed).\nOther ranges: /proactive quiet HHMM-HHMM.", quietText(q))
	return withNotice(Response{Text: text, Buttons: h.rangeRows("quiet", quietChoices, cur)}, notice, code), nil
}

// pausedByQuiet names the presets that quiet hours or the working window left
// without a run time, grouped by the reason stored when they were paused, as a
// sentence to append to a notice ("" when none).
func (h proactiveHandler) pausedByQuiet(ctx context.Context, svc *proactive.Service) string {
	list, err := svc.List(ctx)
	if err != nil {
		return ""
	}
	var out string
	for _, reason := range []string{proactive.ReasonInsideQuiet, proactive.ReasonNoRunHours} {
		var ids []string
		for _, st := range list {
			if st.State == proactive.StatePaused && st.PausedReason == reason {
				ids = append(ids, st.Preset.ID)
			}
		}
		if len(ids) > 0 {
			out += " Paused (" + reason + "): " + titles(ids) + "."
		}
	}
	return out
}
