package control

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/proactive"
)

func windowText(w proactive.Window) string { return fmt.Sprintf("%02d-%02d", w.StartHour, w.EndHour) }

func quietText(q *proactive.Quiet) string {
	if q == nil {
		return "off"
	}
	return minutesText(q.StartMin) + "-" + minutesText(q.EndMin)
}

// scheduleText describes a preset's schedule for a screen.
func scheduleText(p proactive.Preset, s proactive.Settings, env proactive.Env) string {
	if p.Polling() {
		every := strings.TrimSuffix(s.Freq, "m") + " min"
		if s.Freq == "60m" {
			every = "60 min"
		}
		out := fmt.Sprintf("every %s, weekdays %s", every, windowText(env.Window))
		if env.Quiet != nil {
			out += ", minus quiet hours"
		}
		return out
	}
	switch s.Freq {
	case "sun":
		return "Sunday " + fixedTime(s.Time)
	case "mon":
		return "Monday " + fixedTime(s.Time)
	}
	return s.Freq + " " + fixedTime(s.Time)
}

// runsText is the cost line: runs per working day, or per week for weekly presets.
func runsText(p proactive.Preset, s proactive.Settings, env proactive.Env) string {
	n, err := proactive.RunsPerWorkingDay(p, s, env)
	switch {
	case err != nil:
		return "no run time under the current quiet hours"
	case s.Freq == "sun" || s.Freq == "mon":
		return "≈1 run/week"
	case n == 1:
		return "≈1 run/day"
	}
	return fmt.Sprintf("≈%d runs/day", n)
}

func stateText(st proactive.Status) string {
	switch st.State {
	case proactive.StateActive:
		return "on"
	case proactive.StatePaused:
		if st.PausedReason != "" {
			return "paused (" + st.PausedReason + ")"
		}
		return "paused"
	}
	return "off"
}

func (h proactiveHandler) overview(ctx context.Context, svc *proactive.Service, notice string, code int) (Response, error) {
	env, err := h.env(ctx, svc)
	if err != nil {
		return fail(err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		return fail(err)
	}
	paused, err := svc.GloballyPaused(ctx)
	if err != nil {
		return fail(err)
	}
	var sb strings.Builder
	sb.WriteString("**Proactive actions**\n")
	fmt.Fprintf(&sb, "Timezone `%s` · window %s · quiet %s\n", env.TZ, windowText(env.Window), quietText(env.Quiet))
	if paused {
		sb.WriteString("All presets are paused (global pause).\n")
	}
	sb.WriteString("\n")
	for _, c := range connectorTokens {
		on, total := 0, 0
		for _, st := range list {
			if st.Preset.Connector() == c.key {
				total++
				if st.State == proactive.StateActive {
					on++
				}
			}
		}
		fmt.Fprintf(&sb, "%s: %d of %d on\n", c.title, on, total)
	}
	sb.WriteString("\nConnector status is not checked yet: turning a preset on asks you to confirm.")

	pause := Button{Label: "Pause all", Command: "proactive", Args: []string{"off"}}
	if paused {
		pause = Button{Label: "Resume all", Command: "proactive", Args: []string{"on"}, Style: StyleSuccess}
	}
	resp := Response{Text: sb.String(), Buttons: [][]Button{
		{{Label: "Gmail", Command: "proactive", Args: []string{"gmail"}}, {Label: "Calendar", Command: "proactive", Args: []string{"calendar"}}},
		{{Label: "Timezone", Command: "proactive", Args: []string{"tz"}}, {Label: "Hours", Command: "proactive", Args: []string{"hours"}}, {Label: "Quiet", Command: "proactive", Args: []string{"quiet"}}},
		{pause},
	}}
	return withNotice(resp, notice, code), nil
}

var backToOverview = Button{Label: "‹ Back", Command: "proactive"}

func (h proactiveHandler) connectorScreen(ctx context.Context, svc *proactive.Service, key, notice string, code int) (Response, error) {
	env, err := h.env(ctx, svc)
	if err != nil {
		return fail(err)
	}
	list, err := svc.List(ctx)
	if err != nil {
		return fail(err)
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s** · status %s\n\n", connectorTitle(key), h.connector(ctx, key))
	var row []Button
	for _, st := range list {
		if st.Preset.Connector() != key {
			continue
		}
		if reason, off := st.Preset.Unavailable(); off {
			fmt.Fprintf(&sb, "• %s — not available yet (%s)\n", st.Preset.Title, reason)
		} else {
			fmt.Fprintf(&sb, "• %s — %s — %s, %s\n", st.Preset.Title, stateText(st), scheduleText(st.Preset, st.Settings, env), runsText(st.Preset, st.Settings, env))
		}
		tok := presetToken(st.Preset)
		row = append(row, Button{Label: strings.ToUpper(tok[:1]) + tok[1:], Command: "proactive", Args: []string{connectorWord(key), tok}})
	}
	return withNotice(Response{Text: sb.String(), Buttons: [][]Button{row, {backToOverview}}}, notice, code), nil
}

func presetButton(label, current string, args ...string) Button {
	b := Button{Label: label, Command: "proactive", Args: args}
	if current == "yes" {
		b.Label, b.Style = "✓ "+label, StylePrimary
	}
	return b
}

func mark(ok bool) string {
	if ok {
		return "yes"
	}
	return ""
}

func (h proactiveHandler) presetScreen(ctx context.Context, svc *proactive.Service, p proactive.Preset, notice string, code int) (Response, error) {
	if _, err := svc.UpgradeTemplates(ctx); err != nil { // section 6.6: also when a preset screen opens
		h.deps.Log.Warn("proactive: template upgrade on screen open failed", "preset", p.ID, "err", err)
	}
	env, err := h.env(ctx, svc)
	if err != nil {
		return fail(err)
	}
	st, err := svc.Status(ctx, p.ID)
	if err != nil {
		return fail(err)
	}
	tok, word := presetToken(p), connectorWord(p.Connector())
	var sb strings.Builder
	fmt.Fprintf(&sb, "**%s · %s**\n", connectorTitle(p.Connector()), p.Title)
	if reason, off := p.Unavailable(); off && st.State == proactive.StateOff {
		fmt.Fprintf(&sb, "Not available yet: %s.\n", reason)
		return withNotice(Response{Text: sb.String(), Buttons: [][]Button{{{Label: "‹ Back", Command: "proactive", Args: []string{word}}}}}, notice, code), nil
	}
	fmt.Fprintf(&sb, "State: %s\n", stateText(st))
	fmt.Fprintf(&sb, "Schedule: %s\n%s\n", scheduleText(p, st.Settings, env), runsText(p, st.Settings, env))
	if p.ID == "cal.reminder" {
		fmt.Fprintf(&sb, "Reminder lead: %d min\n", st.Settings.Lead)
	}
	if loc, lerr := time.LoadLocation(env.TZ); lerr == nil {
		if st.State == proactive.StateActive && st.NextRun > 0 {
			fmt.Fprintf(&sb, "Next run: %s\n", time.UnixMilli(st.NextRun).In(loc).Format("Mon 15:04"))
		}
		if st.LastRun > 0 {
			fmt.Fprintf(&sb, "Last run: %s\n", time.UnixMilli(st.LastRun).In(loc).Format("Mon 15:04"))
		}
		if line := h.outcomeLine(ctx, p, st.LastRun, loc); line != "" {
			sb.WriteString(line + "\n")
		}
	}
	edited := "no"
	if st.Modified {
		edited = "yes"
	}
	fmt.Fprintf(&sb, "Template v%d · edited in /tasks: %s\n", p.TemplateVersion, edited)
	if st.Modified && st.TemplateOutdated {
		sb.WriteString("Template updated: Reset to template applies it and drops your edits.\n")
	}
	if st.Conflict {
		sb.WriteString("A task of another chat holds this preset's id; it is left alone.\n")
	}

	rows := [][]Button{{
		presetButton("On", mark(st.State == proactive.StateActive), word, tok, "on"),
		presetButton("Off", mark(st.State != proactive.StateActive), word, tok, "off"),
	}}
	if freqs := p.Freqs(); len(freqs) > 1 {
		var row []Button
		for _, f := range freqs {
			row = append(row, presetButton(f, mark(f == st.Settings.Freq), word, tok, "freq", f))
		}
		rows = append(rows, row)
	}
	if len(p.TimeOptions(nil)) > 0 {
		rows = append(rows, []Button{{Label: "Time " + fixedTime(st.Settings.Time), Command: "proactive", Args: []string{word, tok, "time"}}})
	}
	if leads := p.Leads(); len(leads) > 0 {
		var row []Button
		for _, l := range leads {
			row = append(row, presetButton(fmt.Sprintf("%d min", l), mark(l == st.Settings.Lead), word, tok, "lead", fmt.Sprint(l)))
		}
		rows = append(rows, row)
	}
	if st.State != proactive.StateOff && (st.Modified || st.TemplateOutdated) {
		rows = append(rows, []Button{{Label: "Reset to template", Command: "proactive", Args: []string{word, tok, "reset"}, Style: StyleDanger}})
	}
	rows = append(rows, []Button{{Label: "‹ Back", Command: "proactive", Args: []string{word}}})
	return withNotice(Response{Text: sb.String(), Buttons: rows}, notice, code), nil
}

func (h proactiveHandler) timePicker(ctx context.Context, svc *proactive.Service, p proactive.Preset) (Response, error) {
	env, err := h.env(ctx, svc)
	if err != nil {
		return fail(err)
	}
	st, err := svc.Status(ctx, p.ID)
	if err != nil {
		return fail(err)
	}
	tok, word := presetToken(p), connectorWord(p.Connector())
	text := fmt.Sprintf("**%s · %s**\nTime of day in `%s`. Other times: /proactive %s %s time HHMM", connectorTitle(p.Connector()), p.Title, env.TZ, word, tok)
	var rows [][]Button
	var row []Button
	for _, t := range p.TimeOptions(env.Quiet) {
		row = append(row, presetButton(t, mark(t == st.Settings.Time), word, tok, "time", t))
		if len(row) == pickerPerRow {
			rows, row = append(rows, row), nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []Button{{Label: "‹ Back", Command: "proactive", Args: []string{word, tok}}})
	return Response{Text: text, Buttons: rows}, nil
}

func (h proactiveHandler) globalPause(ctx context.Context, svc *proactive.Service, pause bool) (Response, error) {
	if pause {
		if err := svc.PauseAll(ctx); err != nil {
			return fail(err)
		}
		return h.overview(ctx, svc, "All presets paused. Send /proactive on to resume them.", 0)
	}
	rep, err := svc.ResumeAll(ctx)
	if err != nil {
		return fail(err)
	}
	return h.overview(ctx, svc, resumeText(rep), 0)
}

func titles(ids []string) string {
	var out []string
	for _, id := range ids {
		if p, ok := proactive.Lookup(id); ok {
			out = append(out, p.Title)
		}
	}
	return strings.Join(out, ", ")
}

func resumeText(rep proactive.ResumeReport) string {
	parts := []string{"Resumed: " + orNone(titles(rep.Resumed)) + "."}
	if len(rep.StillPaused) > 0 {
		parts = append(parts, "Still paused (inside quiet hours): "+titles(rep.StillPaused)+".")
	}
	if len(rep.Skipped) > 0 {
		parts = append(parts, "Left as they are (changed during the pause): "+titles(rep.Skipped)+".")
	}
	return strings.Join(parts, " ")
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// outcomeLine describes what the last run did, from the outcome file the agent
// wrote (section 5.3). The file is agent-written and untrusted: only its
// sanitized fields are shown, nothing acts on it. A run with no usable file
// (missing, corrupt, from an earlier run) reads "ran, outcome unknown", taken
// from the task's own last_run. A preset that never ran shows nothing.
func (h proactiveHandler) outcomeLine(ctx context.Context, p proactive.Preset, lastRun int64, loc *time.Location) string {
	var o proactive.Outcome
	known := false
	if h.deps.ChatDir != nil {
		if dir, err := h.deps.ChatDir(ctx); err == nil {
			if got, rerr := proactive.ReadOutcome(dir, p.ID); rerr == nil {
				o, known = got, true
			} else if !errors.Is(rerr, fs.ErrNotExist) {
				h.deps.Log.Debug("proactive: outcome file unusable", "preset", p.ID, "err", rerr)
			}
		}
	}
	if known && lastRun > 0 {
		// Written by an earlier run if it is older than this one. The agent's own
		// "at" is used when present; otherwise the file's modification time.
		when := o.At
		if when.IsZero() {
			when = o.Modified
		}
		if !when.IsZero() && when.Before(time.UnixMilli(lastRun).Add(-time.Minute)) {
			known = false
		}
	}
	if known && o.Result == proactive.OutcomeUnknown {
		known = false
	}
	if !known {
		if lastRun == 0 {
			return ""
		}
		return "Last outcome: ran, outcome unknown"
	}
	var what string
	switch o.Result {
	case proactive.OutcomeSent:
		what = "sent a message"
	case proactive.OutcomeEmpty:
		what = "stayed silent (nothing to report)"
	case proactive.OutcomeUnavailable:
		what = "connector not available"
	default:
		what = "error"
	}
	at := time.UnixMilli(lastRun)
	if !o.At.IsZero() {
		at = o.At
	}
	out := fmt.Sprintf("Last outcome: %s (%s)", what, at.In(loc).Format("Mon 15:04"))
	if note := shortNote(noMarkup(o.Note)); note != "" {
		out += " · (agent note, untrusted) `" + note + "`"
	}
	return out
}

// noMarkup removes the characters that would change Markdown or HTML.
func noMarkup(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune("`*_[]<>&", r) {
			return -1
		}
		return r
	}, s)
}

// shortNote caps the agent's note for the screen; the file may carry up to 120
// characters but the owner sees a short single line.
func shortNote(s string) string {
	const max = 80
	if r := []rune(s); len(r) > max {
		return string(r[:max]) + "…"
	}
	return s
}
