package proactive

import "strconv"

// RunsPerWorkingDay is the number of agent turns the preset costs on a
// working day under env (section 5.4). A weekly preset is 0 on a working day.
// It fails exactly when Render does, so a screen can tell the owner why.
func RunsPerWorkingDay(p Preset, s Settings, env Env) (int, error) {
	if _, err := Render(p, s, env); err != nil {
		return 0, err
	}
	if !p.polling {
		if s.Freq == "sun" || s.Freq == "mon" {
			return 0, nil
		}
		return 1, nil
	}
	per := 1
	if s.Freq != "60m" {
		n, _ := strconv.Atoi(s.Freq[:len(s.Freq)-1]) // validated by Render
		per = 60 / n
	}
	return per * len(pollHours(env)), nil
}
