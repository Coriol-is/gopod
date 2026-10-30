package proactive

import (
	"errors"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/scheduler"
)

func mustPreset(t *testing.T, id string) Preset {
	t.Helper()
	p, ok := Lookup(id)
	if !ok {
		t.Fatalf("no preset %s", id)
	}
	return p
}

func env() Env { return Env{TZ: "Europe/Belgrade", Window: DefaultWindow} }

func TestRenderTable(t *testing.T) {
	quiet := func(from, to string) *Quiet {
		q, err := ParseQuiet(from, to)
		if err != nil {
			t.Fatal(err)
		}
		return &q
	}
	cases := []struct {
		name   string
		preset string
		s      Settings
		env    Env
		want   string
	}{
		{"daily 08:00", "gmail.morning", Settings{Freq: "daily", Time: "0800"}, env(), "CRON_TZ=Europe/Belgrade 0 8 * * *"},
		{"weekdays 18:00", "gmail.evening", Settings{Freq: "weekdays", Time: "1800"}, env(), "CRON_TZ=Europe/Belgrade 0 18 * * 1-5"},
		{"minutes kept", "gmail.morning", Settings{Freq: "daily", Time: "0830"}, env(), "CRON_TZ=Europe/Belgrade 30 8 * * *"},
		{"sunday", "cal.weekly", Settings{Freq: "sun", Time: "1800"}, env(), "CRON_TZ=Europe/Belgrade 0 18 * * 0"},
		{"monday", "cal.weekly", Settings{Freq: "mon", Time: "1700"}, env(), "CRON_TZ=Europe/Belgrade 0 17 * * 1"},
		{"15 min in window", "gmail.urgent", Settings{Freq: "15m"}, env(), "CRON_TZ=Europe/Belgrade */15 9-18 * * 1-5"},
		{"30 min in window", "gmail.urgent", Settings{Freq: "30m"}, env(), "CRON_TZ=Europe/Belgrade */30 9-18 * * 1-5"},
		{"60 min in window", "gmail.urgent", Settings{Freq: "60m"}, env(), "CRON_TZ=Europe/Belgrade 0 9-18 * * 1-5"},
		{"reminder poll", "cal.reminder", Settings{Freq: "30m", Lead: 10}, env(), "CRON_TZ=Europe/Belgrade */30 9-18 * * 1-5"},
		{"custom window", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "Asia/Tokyo", Window: Window{8, 20}}, "CRON_TZ=Asia/Tokyo 0 8-19 * * 1-5"},
		{"one-hour window", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "UTC", Window: Window{9, 10}}, "CRON_TZ=UTC 0 9 * * 1-5"},
		{"fixed ignores window", "cal.agenda", Settings{Freq: "daily", Time: "0700"}, Env{TZ: "UTC", Window: Window{9, 10}}, "CRON_TZ=UTC 0 7 * * *"},
		{"overnight quiet outside window changes nothing", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("2200", "0800")}, "CRON_TZ=UTC 0 9-18 * * 1-5"},
		{"midday quiet splits the hours", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("1200", "1400")}, "CRON_TZ=UTC 0 9-11,14-18 * * 1-5"},
		{"partly quiet hour is dropped", "gmail.urgent", Settings{Freq: "30m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("2200", "0930")}, "CRON_TZ=UTC */30 10-18 * * 1-5"},
		{"quiet ending on the hour keeps that hour", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("2200", "1000")}, "CRON_TZ=UTC 0 10-18 * * 1-5"},
		{"isolated hours stay separate", "gmail.urgent", Settings{Freq: "60m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("1000", "1700")}, "CRON_TZ=UTC 0 9,17-18 * * 1-5"},
		{"quiet end is exclusive for fixed times", "gmail.morning", Settings{Freq: "daily", Time: "0800"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: quiet("2200", "0800")}, "CRON_TZ=UTC 0 8 * * *"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := Render(mustPreset(t, c.preset), c.s, c.env)
			if err != nil || got != c.want {
				t.Fatalf("got %q err=%v\nwant %q", got, err, c.want)
			}
			if err := scheduler.ValidateCron(got); err != nil {
				t.Fatalf("scheduler rejects %q: %v", got, err)
			}
		})
	}
}

func TestRenderRefusals(t *testing.T) {
	q := func(from, to string) *Quiet {
		v, err := ParseQuiet(from, to)
		if err != nil {
			t.Fatal(err)
		}
		return &v
	}
	_, err := Render(mustPreset(t, "gmail.morning"), Settings{Freq: "daily", Time: "0700"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: q("0600", "0800")})
	if !errors.Is(err, ErrInsideQuiet) {
		t.Errorf("fixed time in quiet: %v", err)
	}
	_, err = Render(mustPreset(t, "gmail.evening"), Settings{Freq: "daily", Time: "2100"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: q("2000", "2200")})
	if !errors.Is(err, ErrInsideQuiet) {
		t.Errorf("evening time in quiet: %v", err)
	}
	if _, err = Render(mustPreset(t, "gmail.evening"), Settings{Freq: "daily", Time: "2230"}, env()); err == nil {
		t.Error("rendered a time outside its range")
	}
	_, err = Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "60m"}, Env{TZ: "UTC", Window: DefaultWindow, Quiet: q("0900", "1900")})
	if !errors.Is(err, ErrNoRunHours) {
		t.Errorf("quiet covers the window: %v", err)
	}
	if _, err = Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "5m"}, env()); err == nil {
		t.Error("invalid settings rendered")
	}
	if _, err = Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "60m"}, Env{TZ: "Mars/Base", Window: DefaultWindow}); err == nil {
		t.Error("invalid timezone rendered")
	}
	for _, w := range []Window{{9, 9}, {19, 9}, {-1, 5}, {0, 25}} {
		if _, err = Render(mustPreset(t, "gmail.urgent"), Settings{Freq: "60m"}, Env{TZ: "UTC", Window: w}); err == nil {
			t.Errorf("window %+v accepted", w)
		}
	}
}

func TestParseQuiet(t *testing.T) {
	if _, err := ParseQuiet("2200", "2200"); err == nil {
		t.Error("empty quiet window accepted")
	}
	if _, err := ParseQuiet("2500", "0800"); err == nil {
		t.Error("bad time accepted")
	}
	q, err := ParseQuiet("2200", "0800")
	if err != nil {
		t.Fatal(err)
	}
	for min, want := range map[int]bool{22 * 60: true, 23*60 + 59: true, 0: true, 7*60 + 59: true, 8 * 60: false, 12 * 60: false, 21*60 + 59: false} {
		if q.Contains(min) != want {
			t.Errorf("Contains(%d) = %v", min, !want)
		}
	}
}

// A preset at 08:00 Belgrade fires at 08:00 local on both sides of the DST
// changes, and the same preset re-rendered for New York carries its own zone.
func TestRenderedFixedTimeSurvivesDST(t *testing.T) {
	sched, err := Render(mustPreset(t, "gmail.morning"), Settings{Freq: "daily", Time: "0800"}, env())
	if err != nil {
		t.Fatal(err)
	}
	loc, _ := time.LoadLocation("Europe/Belgrade")
	for _, start := range []time.Time{
		time.Date(2026, 10, 23, 12, 0, 0, 0, loc), // late-October fall-back is on the 25th
		time.Date(2027, 3, 26, 12, 0, 0, 0, loc),  // late-March spring-forward is on the 28th
	} {
		now := start
		for i := 0; i < 5; i++ {
			ms, err := scheduler.FirstRunAt("cron", sched, now)
			if err != nil {
				t.Fatal(err)
			}
			next := time.UnixMilli(ms).In(loc)
			if next.Hour() != 8 || next.Minute() != 0 {
				t.Fatalf("after %s next run is %s, want 08:00 local", now, next)
			}
			now = next
		}
	}
	ny, err := Render(mustPreset(t, "gmail.morning"), Settings{Freq: "daily", Time: "0800"}, Env{TZ: "America/New_York", Window: DefaultWindow})
	if err != nil || ny != "CRON_TZ=America/New_York 0 8 * * *" {
		t.Fatalf("got %q err=%v", ny, err)
	}
}
