package proactive

import "testing"

func TestRunsPerWorkingDay(t *testing.T) {
	env := Env{TZ: DefaultTimezone, Window: DefaultWindow}
	quiet := Quiet{StartMin: 22 * 60, EndMin: 9*60 + 30} // drops hour 09
	cases := []struct {
		id   string
		set  Settings
		env  Env
		want int
	}{
		{"gmail.morning", Settings{Freq: "daily", Time: "0800"}, env, 1},
		{"gmail.urgent", Settings{Freq: "60m"}, env, 10},
		{"gmail.urgent", Settings{Freq: "30m"}, env, 20},
		{"gmail.urgent", Settings{Freq: "15m"}, env, 40},
		{"cal.reminder", Settings{Freq: "30m", Lead: 10}, env, 20},
		{"gmail.urgent", Settings{Freq: "60m"}, Env{TZ: DefaultTimezone, Window: DefaultWindow, Quiet: &quiet}, 9},
		{"cal.weekly", Settings{Freq: "sun", Time: "1800"}, env, 0},
	}
	for _, c := range cases {
		p, _ := Lookup(c.id)
		got, err := RunsPerWorkingDay(p, c.set, c.env)
		if err != nil || got != c.want {
			t.Errorf("%s %+v: got %d, %v; want %d", c.id, c.set, got, err, c.want)
		}
	}
	p, _ := Lookup("gmail.evening")
	q := Quiet{StartMin: 17 * 60, EndMin: 19 * 60}
	if _, err := RunsPerWorkingDay(p, Settings{Freq: "weekdays", Time: "1800"}, Env{TZ: DefaultTimezone, Window: DefaultWindow, Quiet: &q}); err == nil {
		t.Error("a time inside quiet hours must fail like Render")
	}
}
