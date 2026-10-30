package proactive

import (
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata" // IANA zones must work in the minimal runtime image.
)

// DefaultTimezone is the zone used until the owner sets one (owner decision
// 2026-10-30). There is no process-wide zone, so every preset schedule carries
// its own CRON_TZ.
const DefaultTimezone = "Europe/Belgrade"

// ValidateTimezone is the single check for a typed zone and a picker press.
func ValidateTimezone(name string) error {
	switch {
	case name == "":
		return errors.New("empty timezone")
	case len(name) > 64:
		return errors.New("timezone name is longer than 64 bytes")
	case strings.ContainsAny(name, " \t\r\n\v\f"):
		return errors.New("timezone name contains whitespace")
	case name == "Local":
		return errors.New("Local is not an IANA zone")
	}
	if _, err := time.LoadLocation(name); err != nil {
		return fmt.Errorf("unknown timezone %q: %w", name, err)
	}
	return nil
}
