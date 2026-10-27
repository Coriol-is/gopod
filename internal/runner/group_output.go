package runner

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrGroupOperationUnavailable is intentionally free of native stderr, Docker
// request details, credential paths and capability-bearing endpoints.
var ErrGroupMaintenanceUnavailable = errors.New("runner: group maintenance unavailable; context preserved")

var ErrGroupOperationUnavailable = errors.New("runner: group operation unavailable")

func restrictedGroupProvider(prov AgentProvider) bool {
	_, ok := prov.(interface{ GroupExecEnv() []string })
	return ok
}
func groupPublicError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, ErrUsageLimit):
		var usage *UsageLimitError
		if errors.As(err, &usage) && (usage.Provider == "claude" || usage.Provider == "codex") {
			return &UsageLimitError{Provider: usage.Provider}
		}
		return ErrUsageLimit
	case errors.Is(err, ErrNotLoggedIn):
		return ErrNotLoggedIn
	case errors.Is(err, ErrSessionExpired):
		return ErrSessionExpired
	case errors.Is(err, ErrGroupMaintenanceUnavailable):
		return ErrGroupMaintenanceUnavailable
	case errors.Is(err, ErrProfileUnavailable):
		return ErrProfileUnavailable
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return ErrGroupOperationUnavailable
	}
}

var groupCapabilityURL = regexp.MustCompile(`http://gopod-relay:8787/mcp/[A-Za-z0-9_-]{32,128}|http://group:[A-Za-z0-9_-]{32,128}@gopod-relay:8787`)

func groupCapabilityRanges(text string, complete bool) [][]int {
	matches := groupCapabilityURL.FindAllStringIndex(text, -1)
	out := matches[:0]
	for _, m := range matches {
		proxy := strings.HasPrefix(text[m[0]:], "http://group:")
		if !proxy {
			if m[1] == len(text) && !complete {
				continue
			}
			if m[1] < len(text) && capabilityChar(text[m[1]]) {
				continue
			}
		} else if m[1] < len(text) && text[m[1]] >= '0' && text[m[1]] <= '9' {
			continue
		}
		out = append(out, m)
	}
	return out
}
func capabilityChar(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_'
}
func redactGroupCapabilities(text string) string {
	matches := groupCapabilityRanges(text, true)
	if len(matches) == 0 {
		return text
	}
	var out strings.Builder
	last := 0
	for _, m := range matches {
		out.WriteString(text[last:m[0]])
		out.WriteString("[internal capability]")
		last = m[1]
	}
	out.WriteString(text[last:])
	return out.String()
}

// Hold a small suffix so URLs split at arbitrary native chunk boundaries never
// expose their capability. The longest approved URL is under 200 bytes.
type groupChunkFilter struct{ pending string }

func (f *groupChunkFilter) Push(chunk string) string {
	f.pending += chunk
	cut := len(f.pending) - 256
	if cut <= 0 {
		return ""
	}
	for _, m := range groupCapabilityRanges(f.pending, false) {
		if m[0] < cut && m[1] > cut {
			cut = m[0]
			break
		}
	}
	for cut > 0 && !utf8.RuneStart(f.pending[cut]) {
		cut--
	}
	emit := redactGroupCapabilities(f.pending[:cut])
	f.pending = f.pending[cut:]
	return emit
}
func (f *groupChunkFilter) Flush() string {
	out := redactGroupCapabilities(f.pending)
	f.pending = ""
	return out
}

func outwardProviderError(prov AgentProvider, err error) error {
	if restrictedGroupProvider(prov) {
		return groupPublicError(err)
	}
	return err
}
func groupOutputText(prov AgentProvider, text string) string {
	if restrictedGroupProvider(prov) {
		return redactGroupCapabilities(text)
	}
	return text
}

func groupPublicStderr(prov AgentProvider, text string) string {
	if restrictedGroupProvider(prov) {
		return ""
	}
	return text
}
