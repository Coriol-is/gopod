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
