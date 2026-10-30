package proactive

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var (
	// ErrInsideQuiet means a fixed-time preset would run inside quiet hours.
	ErrInsideQuiet = errors.New("proactive: time is inside quiet hours")
	// ErrNoRunHours means quiet hours remove every hour of the working window.
	ErrNoRunHours = errors.New("proactive: no run hours left outside quiet hours")
)

// Window is the working window of the polling presets: weekdays, whole hours,
// StartHour inclusive and EndHour exclusive (owner default 09-19).
type Window struct{ StartHour, EndHour int }

// DefaultWindow is weekdays 09-19.
var DefaultWindow = Window{StartHour: 9, EndHour: 19}

func (w Window) validate() error {
	if w.StartHour < 0 || w.EndHour > 24 || w.StartHour >= w.EndHour {
		return fmt.Errorf("working window %02d-%02d is invalid", w.StartHour, w.EndHour)
	}
	return nil
}

// Quiet is a daily window in minutes after midnight in which presets do not
// run. Start is inclusive and End exclusive; Start > End wraps over midnight.
type Quiet struct{ StartMin, EndMin int }

// ParseQuiet builds a Quiet from two HHMM strings.
func ParseQuiet(from, to string) (Quiet, error) {
	s, err := parseHHMM(from)
	if err != nil {
		return Quiet{}, err
	}
	e, err := parseHHMM(to)
	if err != nil {
		return Quiet{}, err
	}
	q := Quiet{StartMin: s, EndMin: e}
	return q, q.validate()
}

func (q Quiet) validate() error {
	if q.StartMin < 0 || q.StartMin >= 24*60 || q.EndMin < 0 || q.EndMin >= 24*60 {
		return fmt.Errorf("quiet window %d-%d is outside the day", q.StartMin, q.EndMin)
	}
	if q.StartMin == q.EndMin {
		return errors.New("quiet window is empty")
	}
	return nil
}

// Contains reports whether a minute of day lies inside the quiet window.
func (q Quiet) Contains(min int) bool {
	if q.StartMin < q.EndMin {
		return min >= q.StartMin && min < q.EndMin
	}
	return min >= q.StartMin || min < q.EndMin
}

// overlapsHour reports whether any minute of hour h is quiet.
func (q Quiet) overlapsHour(h int) bool {
	for m := h * 60; m < h*60+60; m++ {
		if q.Contains(m) {
			return true
		}
	}
	return false
}

// Env is everything besides the preset and its settings that shapes a schedule.
type Env struct {
	TZ     string
	Window Window
	Quiet  *Quiet // nil when quiet hours are off
}

// Render produces the scheduler cron text for a preset (section 7). It is a
// pure function: the scheduler is untouched.
//
// Polling presets run every N minutes on weekdays during the working window
// minus quiet hours; an hour that is even partly quiet is dropped (the safe
// reading of "do not run inside the window"). Fixed-time presets run at their
// time and fail with ErrInsideQuiet if it falls in the quiet window.
func Render(p Preset, s Settings, env Env) (string, error) {
	if err := p.ValidateSettings(s); err != nil {
		return "", err
	}
	if err := ValidateTimezone(env.TZ); err != nil {
		return "", err
	}
	prefix := "CRON_TZ=" + env.TZ + " "

	if p.polling {
		if err := env.Window.validate(); err != nil {
			return "", err
		}
		hours := pollHours(env)
		if len(hours) == 0 {
			return "", ErrNoRunHours
		}
		minute := "0"
		if s.Freq != "60m" {
			minute = "*/" + strings.TrimSuffix(s.Freq, "m")
		}
		return prefix + minute + " " + hourRanges(hours) + " * * 1-5", nil
	}

	min, _ := parseHHMM(s.Time) // validated above
	if env.Quiet != nil && env.Quiet.Contains(min) {
		return "", fmt.Errorf("%w: %s", ErrInsideQuiet, s.Time)
	}
	dow := map[string]string{"daily": "*", "weekdays": "1-5", "sun": "0", "mon": "1"}[s.Freq]
	return fmt.Sprintf("%s%d %d * * %s", prefix, min%60, min/60, dow), nil
}

// hourRanges joins ascending hours into cron ranges: 9,10,11,14 -> "9-11,14".
func hourRanges(hours []int) string {
	var parts []string
	for i := 0; i < len(hours); {
		j := i
		for j+1 < len(hours) && hours[j+1] == hours[j]+1 {
			j++
		}
		if j > i {
			parts = append(parts, strconv.Itoa(hours[i])+"-"+strconv.Itoa(hours[j]))
		} else {
			parts = append(parts, strconv.Itoa(hours[i]))
		}
		i = j + 1
	}
	return strings.Join(parts, ",")
}

// pollHours lists the working-window hours left after quiet hours.
func pollHours(env Env) []int {
	var hours []int
	for h := env.Window.StartHour; h < env.Window.EndHour; h++ {
		if env.Quiet == nil || !env.Quiet.overlapsHour(h) {
			hours = append(hours, h)
		}
	}
	return hours
}
