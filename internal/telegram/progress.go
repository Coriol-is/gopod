package telegram

import (
	"regexp"
	"strings"
	"unicode/utf8"

	gopodlog "github.com/Coriol-is/gopod/internal/log"
	"github.com/Coriol-is/gopod/internal/runner"
)

// Progress lines: while a structured agent run has produced no answer
// text, the draft (or the edit-path placeholder) shows its last tool
// steps, one per line. A line names the tool and its main input, never
// tool output or file contents.

const (
	// progressShown is how many recent steps the draft shows.
	progressShown = 5
	// progressLineMax caps a line, emoji included, in characters.
	progressLineMax = 60
	// agentWorkdir is the chat workspace inside the container; paths
	// under it are shown relative.
	agentWorkdir = "/workspace/chat/"
)

// progressIcon maps a tool to its line prefix; other tools get 🔧.
var progressIcon = map[string]string{
	"Bash":         "⚙️",
	"Edit":         "✏️",
	"MultiEdit":    "✏️",
	"Write":        "✏️",
	"NotebookEdit": "✏️",
	"Read":         "📖",
	"Grep":         "🔎",
	"Glob":         "🔎",
	"WebSearch":    "🌐",
	"WebFetch":     "🌐",
}

// pathTools show a file path, cut from the left when too long so the
// file name stays visible.
var pathTools = map[string]bool{
	"Edit": true, "MultiEdit": true, "Write": true, "NotebookEdit": true, "Read": true,
}

// progressLine renders one tool step as a plain-text line of at most
// progressLineMax characters, with secrets redacted.
func progressLine(ev runner.ProgressEvent) string {
	icon, ok := progressIcon[ev.Tool]
	if !ok {
		return truncateRunes("🔧 "+ev.Tool, progressLineMax)
	}
	detail := ev.Detail
	if i := strings.IndexByte(detail, '\n'); i >= 0 {
		detail = strings.TrimSpace(detail[:i]) + " …" // first line of a script
	}
	detail = redactSecrets(strings.Join(strings.Fields(detail), " "))
	if detail == "" {
		detail = ev.Tool
	}
	prefix := icon + " "
	room := progressLineMax - utf8.RuneCountInString(prefix)
	if pathTools[ev.Tool] {
		return prefix + truncateRunesLeft(strings.TrimPrefix(detail, agentWorkdir), room)
	}
	return prefix + truncateRunes(detail, room)
}

// progressView is the draft text for the recent steps.
func progressView(steps []string) string {
	return strings.Join(steps, "\n") + "\n"
}

// progressSecretNames are the D012 words plus names that hold secrets
// in commands and URLs. Progress lines only: the shared D012 list
// (log.SecretWords) stays as it is.
const progressSecretNames = gopodlog.SecretWords + `|pass|pw|passwd|sig|signature|credential`

// Secret patterns for progress lines.
var (
	// NAME=value, NAME: value, "name":"value", \"name\":\"value\",
	// --name=value, URL query parameters (the value stops at &).
	secretAssign = regexp.MustCompile(`(?i)([\w.-]*(?:` + progressSecretNames + `)[\w.-]*(?:\\?")?)(\s*[=:]\s*)(\\"[^"\\]*\\"|"[^"]*"|'[^']*'|[^\s"'\\&]+)`)
	// --token value, -password value.
	secretFlag = regexp.MustCompile(`(?i)(\s--?[\w-]*(?:` + progressSecretNames + `)[\w-]*)(\s+)("[^"]*"|'[^']*'|[^\s"'-]\S*)`)
	// Authorization schemes.
	secretScheme = regexp.MustCompile(`(?i)\b(bearer|basic)(\s+)[^\s"']+`)
	// https://user:password@host
	secretUserinfo = regexp.MustCompile(`(://[^/\s:@]+:)[^@\s/]+@`)
	// Telegram bot API URLs: /bot<id>:<token>/method.
	secretBotURL = regexp.MustCompile(`(?i)/bot\d+:\S*`)
	// Slack incoming webhooks.
	secretSlackHook = regexp.MustCompile(`(?i)(hooks\.slack\.com/services/)\S*`)
	// curl -u user:pass, --user user:pass, --user=user:pass.
	secretUserFlag = regexp.MustCompile(`(\s(?:-u|--user)(?:\s+|=))("[^"]*:[^"]*"|'[^']*:[^']*'|[^\s"']*:\S*)`)
	// sshpass -p SECRET / -pSECRET.
	secretSSHPass = regexp.MustCompile(`(\bsshpass\s+-p\s*)(\S+)`)
	// mysql-style -pSECRET (value attached), only in mysql/mariadb
	// command lines: elsewhere -p... is an ordinary flag.
	mysqlCmd       = regexp.MustCompile(`(?i)\b(?:mysql\w*|mariadb\w*)\b`)
	secretMysqlPwd = regexp.MustCompile(`(\s-p)(\S+)`)
	// aws configure set <secret-named key> VALUE.
	secretAWSSet = regexp.MustCompile(`(?i)(\bconfigure\s+set\s+[\w.-]*(?:` + progressSecretNames + `)[\w.-]*\s+)(\S+)`)
	// Well-known token shapes appearing bare.
	secretToken = regexp.MustCompile(`\b(?:sk-[\w-]{8,}|sk_(?:live|test)_\w{8,}|rk_(?:live|test)_\w{8,}|gh[pousr]_\w{8,}|github_pat_\w{8,}|glpat-[\w-]{8,}|AIza[\w-]{20,}|xox[abprs]-[\w-]{8,}|AKIA[0-9A-Z]{12,}|eyJ[\w-]{8,}\.[\w-]+\.[\w-]+)`)
)

const redacted = "***"

// redactSecrets masks values that look like secrets in a command line.
// It errs towards masking: a progress line is a hint, not a log.
func redactSecrets(s string) string {
	s = secretBotURL.ReplaceAllString(s, "/bot"+redacted)
	s = secretSlackHook.ReplaceAllString(s, "${1}"+redacted)
	s = secretScheme.ReplaceAllString(s, "${1}${2}"+redacted)
	s = secretUserinfo.ReplaceAllString(s, "${1}"+redacted+"@")
	s = secretUserFlag.ReplaceAllString(s, "${1}"+redacted)
	s = secretSSHPass.ReplaceAllString(s, "${1}"+redacted)
	if mysqlCmd.MatchString(s) {
		s = secretMysqlPwd.ReplaceAllString(s, "${1}"+redacted)
	}
	s = secretAWSSet.ReplaceAllString(s, "${1}"+redacted)
	s = secretAssign.ReplaceAllString(s, "${1}${2}"+redacted)
	s = secretFlag.ReplaceAllString(s, "${1}${2}"+redacted)
	return secretToken.ReplaceAllString(s, redacted)
}

// truncateRunes cuts s to at most n characters, ending with "…" when cut.
func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n-1]) + "…"
}

// truncateRunesLeft cuts s to at most n characters, keeping the end and
// starting with "…" when cut.
func truncateRunesLeft(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return "…" + string(r[len(r)-n+1:])
}
