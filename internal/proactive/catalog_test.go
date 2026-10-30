package proactive

import (
	"strings"
	"testing"
)

func TestCatalogIsExactlyTheSixPresets(t *testing.T) {
	var ids []string
	for _, p := range Catalog() {
		ids = append(ids, p.ID)
	}
	want := "gmail.morning gmail.urgent gmail.evening cal.agenda cal.reminder cal.weekly"
	if got := strings.Join(ids, " "); got != want {
		t.Fatalf("catalog = %q, want %q", got, want)
	}
}

func TestCatalogEntriesAreValid(t *testing.T) {
	seen := map[string]bool{}
	for _, p := range Catalog() {
		if seen[p.ID] {
			t.Errorf("duplicate id %s", p.ID)
		}
		seen[p.ID] = true
		if p.TemplateVersion < 1 || p.Title == "" || p.body == "" {
			t.Errorf("%s: incomplete entry", p.ID)
		}
		if !strings.HasPrefix(p.ID, "gmail.") && !strings.HasPrefix(p.ID, "cal.") {
			t.Errorf("%s: unknown connector prefix", p.ID)
		}
		if err := p.ValidateSettings(p.DefaultSettings()); err != nil {
			t.Errorf("%s: default settings invalid: %v", p.ID, err)
		}
		prompt := p.Prompt(PromptInput{TZ: "Europe/Belgrade", OwnerJID: "tg:1", Settings: p.DefaultSettings()})
		if strings.Contains(prompt, "{{") {
			t.Errorf("%s: unresolved placeholder in prompt", p.ID)
		}
		if !strings.Contains(prompt, "untrusted data") || !strings.Contains(prompt, "tg:1") {
			t.Errorf("%s: fixed paragraphs missing", p.ID)
		}
	}
}

func TestOwnerDecisionDefaults(t *testing.T) {
	cases := map[string]Settings{
		"gmail.morning": {Freq: "daily", Time: "0800"},
		"gmail.urgent":  {Freq: "60m"},
		"gmail.evening": {Freq: "weekdays", Time: "1800"},
		"cal.agenda":    {Freq: "daily", Time: "0800"},
		"cal.reminder":  {Freq: "30m", Lead: 10},
		"cal.weekly":    {Freq: "sun", Time: "1800"},
	}
	for id, want := range cases {
		p, ok := Lookup(id)
		if !ok {
			t.Fatalf("%s missing", id)
		}
		if got := p.DefaultSettings(); got != want {
			t.Errorf("%s default = %+v, want %+v", id, got, want)
		}
	}
	if g, _ := Lookup("gmail.urgent"); !g.Polling() {
		t.Error("gmail.urgent must be a polling preset")
	}
	if g, _ := Lookup("cal.agenda"); g.Polling() {
		t.Error("cal.agenda must not be tied to the working window")
	}
}

func TestValidateSettings(t *testing.T) {
	urgent, _ := Lookup("gmail.urgent")
	morning, _ := Lookup("gmail.morning")
	rem, _ := Lookup("cal.reminder")
	weekly, _ := Lookup("cal.weekly")
	bad := []struct {
		name string
		p    Preset
		s    Settings
	}{
		{"unknown freq", urgent, Settings{Freq: "5m"}},
		{"time on poll preset", urgent, Settings{Freq: "30m", Time: "0900"}},
		{"time below range", morning, Settings{Freq: "daily", Time: "0559"}},
		{"time above range", morning, Settings{Freq: "daily", Time: "1101"}},
		{"malformed time", morning, Settings{Freq: "daily", Time: "8am"}},
		{"minute 60", morning, Settings{Freq: "daily", Time: "0860"}},
		{"lead on non-reminder", morning, Settings{Freq: "daily", Time: "0800", Lead: 10}},
		{"unknown lead", rem, Settings{Freq: "30m", Lead: 5}},
		{"weekly freq daily", weekly, Settings{Freq: "daily", Time: "1800"}},
	}
	for _, c := range bad {
		if err := c.p.ValidateSettings(c.s); err == nil {
			t.Errorf("%s: accepted %+v", c.name, c.s)
		}
	}
	for _, c := range []struct {
		p Preset
		s Settings
	}{
		{urgent, Settings{Freq: "15m"}}, {morning, Settings{Freq: "weekdays", Time: "1100"}},
		{rem, Settings{Freq: "30m", Lead: 60}}, {weekly, Settings{Freq: "mon", Time: "1600"}},
	} {
		if err := c.p.ValidateSettings(c.s); err != nil {
			t.Errorf("%s rejected %+v: %v", c.p.ID, c.s, err)
		}
	}
}

func TestReminderLookaheadNeverBelowLeadPlusPoll(t *testing.T) {
	rem, _ := Lookup("cal.reminder")
	for lead, want := range map[int]int{10: 60, 15: 60, 30: 60, 60: 90} {
		if got := rem.Lookahead(lead); got != want {
			t.Errorf("lookahead(lead %d) = %d, want %d", lead, got, want)
		}
	}
	p := rem.Prompt(PromptInput{TZ: "UTC", OwnerJID: "tg:1", Settings: Settings{Freq: "30m", Lead: 60}})
	if !strings.Contains(p, "90 minutes") || !strings.Contains(p, "60 minutes") {
		t.Errorf("lead/lookahead not written into the poll prompt:\n%s", p)
	}
}
