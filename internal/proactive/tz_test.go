package proactive

import (
	"strings"
	"testing"
)

func TestValidateTimezone(t *testing.T) {
	for _, ok := range []string{"Europe/Belgrade", "UTC", "Asia/Tbilisi", "America/New_York"} {
		if err := ValidateTimezone(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "Mars/Base", "Europe/Belgrad", "Local", "Europe/Belgrade extra", strings.Repeat("a", 65), "Europe/Bel\tgrade"} {
		if err := ValidateTimezone(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestValidateTimezoneShapeAllowlist(t *testing.T) {
	for _, ok := range []string{"Europe/Belgrade", "America/Argentina/Buenos_Aires", "Etc/GMT+3", "UTC", "Asia/Tbilisi"} {
		if err := ValidateTimezone(ok); err != nil {
			t.Errorf("%q rejected: %v", ok, err)
		}
	}
	// Host-only or path-like names must never reach LoadLocation: the minimal
	// image has no host zoneinfo, so they would later fail every screen.
	for _, bad := range []string{"../etc/passwd", "/etc/localtime", "localtime", "posixrules", "europe/belgrade", "Европа/Белград", "Europe/Бел", "Europe//Belgrade", "Europe/..", "GMT0 ", "utc"} {
		if err := ValidateTimezone(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}
