package mountsec

import (
	"regexp"
	"runtime"
	"strings"
)

// blockedPatterns is the compiled-in deny list. Even the owner cannot
// mount any host path that resolves (after symlink resolution) to a
// path matching one of these patterns.
//
// Glob semantics, custom (not filepath.Match — that doesn't support **):
//   - "**" matches any number of path segments, including zero
//   - "*"  matches one path segment (no slash)
//   - "?"  matches one character (no slash)
//   - other characters are literal
//
// Mirrors ISOLATION.md §5. Edits to this list are an ADR-worthy event:
// adding a pattern is fine; removing one to permit a previously-blocked
// path needs justification because it's expanding the trust surface.
var blockedPatterns = []string{
	// SSH / GPG / cloud creds
	"**/.ssh/**",
	"**/.ssh",
	"**/.gnupg/**",
	"**/.gnupg",
	"**/.aws/**",
	"**/.aws",

	// Container runtime config
	"**/.docker/**",
	"**/.docker",

	// gopod's own config + secrets surface
	"**/.config/gopod/**",

	// Generic secret-shaped files
	"**/credentials",
	"**/credentials.json",
	"**/id_rsa",
	"**/id_rsa.*",
	"**/id_ed25519",
	"**/id_ed25519.*",
	"**/.netrc",
	"**/.pgpass",
	"**/.env",
	"**/.env.*",

	// System sensitive (Linux container hosts)
	"/etc/shadow",
	"/etc/sudoers",
	"/etc/sudoers.d/**",
	"/proc/**",
	"/sys/**",
	"/dev/**",
}

// blockedRegexps is blockedPatterns compiled to regexes once at package
// init. Compilation failures here are programmer errors and panic.
var blockedRegexps = func() []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(blockedPatterns))
	for i, p := range blockedPatterns {
		out[i] = compileGlob(p)
	}
	return out
}()

// IsBlocked reports whether resolvedPath matches any compiled-in blocked
// pattern. The caller must pass a path that has already gone through
// filepath.EvalSymlinks — IsBlocked does not do its own resolution
// because in tests we sometimes pass synthetic paths that don't exist on
// disk.
//
// On macOS the comparison is case-insensitive (HFS+/APFS default). On
// Linux it is case-sensitive. Mirrors ISOLATION.md §5 last paragraph.
func IsBlocked(resolvedPath string) bool {
	candidate := resolvedPath
	if isCaseInsensitiveFS() {
		candidate = strings.ToLower(candidate)
	}
	for _, re := range blockedRegexps {
		if re.MatchString(candidate) {
			return true
		}
	}
	return false
}

// compileGlob converts a glob pattern (with **, *, ?) into an anchored
// regular expression. Anchoring uses ^ and $ so partial matches do not
// count — `**/.ssh` must match an entire path, not a substring.
//
// On case-insensitive filesystems we lowercase BOTH the pattern and the
// candidate so the regex itself stays case-sensitive (avoids regex (?i)
// surprises across locales).
func compileGlob(pattern string) *regexp.Regexp {
	src := pattern
	if isCaseInsensitiveFS() {
		src = strings.ToLower(src)
	}

	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(src); i++ {
		c := src[i]
		switch c {
		case '*':
			if i+1 < len(src) && src[i+1] == '*' {
				// "**" matches any number of segments including zero,
				// so it should also swallow the slash that follows or
				// precedes it. Easiest correct expansion: ".*".
				b.WriteString(".*")
				i++
			} else {
				// "*" matches one path segment — no slash.
				b.WriteString("[^/]*")
			}
		case '?':
			b.WriteString("[^/]")
		case '.', '+', '(', ')', '|', '^', '$', '{', '}', '[', ']', '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		// blockedPatterns is compiled-in; bad regex == programmer bug.
		panic("mountsec: compileGlob: " + pattern + ": " + err.Error())
	}
	return re
}

// isCaseInsensitiveFS reports whether the host filesystem is conventionally
// case-insensitive. macOS HFS+/APFS volumes default to case-insensitive
// (case-preserving), so a mount of /Users/me/.SSH/key would resolve to
// the same inode as /Users/me/.ssh/key. We treat the comparison as
// case-insensitive to defeat that obfuscation.
//
// Note: a small fraction of macOS users format APFS as case-sensitive on
// purpose. They get a slightly more permissive matcher than necessary,
// which is harmless — case-insensitive matching is strictly stronger.
func isCaseInsensitiveFS() bool {
	return runtime.GOOS == "darwin" || runtime.GOOS == "windows"
}
