package runner

import (
	"errors"
	"strings"
)

// ErrUsageLimit means the provider refused a run because its usage allowance is exhausted.
var ErrUsageLimit = errors.New("runner: provider usage limit reached")

// UsageLimitError identifies the provider of the failed run, even if another
// conversation changes the chat's selected provider before the UI handles it.
type UsageLimitError struct{ Provider string }

func (e *UsageLimitError) Error() string { return "runner: " + e.Provider + " usage limit reached" }
func (e *UsageLimitError) Unwrap() error { return ErrUsageLimit }

// Only inspect stderr and the main assistant/result text, never tool output.
func isUsageLimit(parts ...string) bool {
	for _, part := range parts {
		s := strings.ToLower(part)
		if strings.Contains(s, "you've hit your session limit") || strings.Contains(s, "you’ve hit your session limit") || strings.Contains(s, "you've hit your usage limit") || strings.Contains(s, "you have hit your usage limit") {
			return true
		}
	}
	return false
}
