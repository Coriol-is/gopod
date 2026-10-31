package log

import (
	"log/slog"
	"os"
	"regexp"
	"sort"
	"strings"
)

var telegramCredential = regexp.MustCompile(`(?i)(bot|/file/bot)[0-9]+:[A-Za-z0-9_-]+`)
var bearerCredential = regexp.MustCompile(`(?i)Bearer\s+[A-Za-z0-9._~+/=-]+`)

// Redactor snapshots configured secret values without keeping environment names
// in log output. Create it after configuration loads; never log its contents.
type Redactor struct{ secrets []string }

func NewRedactor() *Redactor {
	r := &Redactor{}
	for _, entry := range os.Environ() {
		name, value, ok := strings.Cut(entry, "=")
		if ok && len(value) >= 4 && secretPattern.MatchString(name) {
			r.secrets = append(r.secrets, value)
		}
	}
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
	return r
}

func (r *Redactor) Text(s string) string {
	for _, secret := range r.secrets {
		s = strings.ReplaceAll(s, secret, "<redacted>")
	}
	s = telegramCredential.ReplaceAllString(s, "bot<redacted>")
	return bearerCredential.ReplaceAllString(s, "Bearer <redacted>")
}

// ReplaceAttr is shared by the stderr handler and the SQLite sink. It resolves
// LogValuer/error values and walks groups before either sink sees the record.
func (r *Redactor) ReplaceAttr(groups []string, a slog.Attr) slog.Attr {
	a.Value = a.Value.Resolve()
	secretGroup := false
	for _, group := range groups {
		secretGroup = secretGroup || secretPattern.MatchString(group)
	}
	if secretGroup || secretPattern.MatchString(a.Key) {
		a.Value = slog.StringValue("<redacted>")
		return a
	}
	switch a.Value.Kind() {
	case slog.KindGroup:
		attrs := a.Value.Group()
		clean := make([]slog.Attr, len(attrs))
		for i, child := range attrs {
			clean[i] = r.ReplaceAttr(nil, child)
		}
		a.Value = slog.GroupValue(clean...)
	case slog.KindString:
		a.Value = slog.StringValue(r.Text(a.Value.String()))
	case slog.KindAny:
		a.Value = slog.StringValue(r.Text(a.Value.String()))
	}
	return a
}
