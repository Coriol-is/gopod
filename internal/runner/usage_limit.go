package runner

import (
	"errors"
	"strings"
)

// ErrUsageLimit means the provider refused a run because its usage allowance is exhausted.
var ErrUsageLimit = errors.New("runner: provider usage limit reached")

// Only inspect stderr and the main assistant/result text, never tool output.
func isUsageLimit(parts ...string) bool {
	for _, part := range parts {
		s := strings.ToLower(part)
		if strings.Contains(s, "you've hit your session limit") || strings.Contains(s, "you’ve hit your session limit") || strings.Contains(s, "you've hit your usage limit") {
			return true
		}
	}
	return false
}
