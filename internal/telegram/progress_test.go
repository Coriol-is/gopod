package telegram

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/runner"
)

func TestProgressLineMapping(t *testing.T) {
	cases := []struct {
		tool, detail, want string
	}{
		{"Bash", "go test ./...", "⚙️ go test ./..."},
		{"Edit", "/workspace/chat/internal/a.go", "✏️ internal/a.go"},
		{"Write", "/workspace/chat/b.py", "✏️ b.py"},
		{"MultiEdit", "/etc/c.conf", "✏️ /etc/c.conf"},
		{"Read", "/workspace/chat/note.txt", "📖 note.txt"},
		{"Grep", "func main", "🔎 func main"},
		{"Glob", "**/*.go", "🔎 **/*.go"},
		{"WebSearch", "go 1.26 release", "🌐 go 1.26 release"},
		{"WebFetch", "https://go.dev/doc", "🌐 https://go.dev/doc"},
		{"TodoWrite", "", "🔧 TodoWrite"},
		{"mcp__memory__search", "ignored", "🔧 mcp__memory__search"},
		{"Bash", "", "⚙️ Bash"},
		{"Bash", "cat <<EOF > x.go\npackage main\nEOF", "⚙️ cat <<EOF > x.go …"},
		{"Bash", "ls   -la\t/tmp", "⚙️ ls -la /tmp"},
	}
	for _, c := range cases {
		if got := progressLine(runner.ProgressEvent{Tool: c.tool, Detail: c.detail}); got != c.want {
			t.Errorf("%s %q: got %q, want %q", c.tool, c.detail, got, c.want)
		}
	}
}

func TestProgressLineTruncates(t *testing.T) {
	long := strings.Repeat("ab ", 50)
	got := progressLine(runner.ProgressEvent{Tool: "Bash", Detail: "echo " + long})
	if n := utf8.RuneCountInString(got); n > progressLineMax {
		t.Errorf("line has %d chars, max %d: %q", n, progressLineMax, got)
	}
	if !strings.HasPrefix(got, "⚙️ echo ab") || !strings.HasSuffix(got, "…") {
		t.Errorf("command not cut at the end: %q", got)
	}

	path := "/workspace/chat/" + strings.Repeat("dir/", 20) + "file_name.go"
	got = progressLine(runner.ProgressEvent{Tool: "Edit", Detail: path})
	if n := utf8.RuneCountInString(got); n > progressLineMax {
		t.Errorf("line has %d chars: %q", n, got)
	}
	if !strings.HasPrefix(got, "✏️ …") || !strings.HasSuffix(got, "/file_name.go") {
		t.Errorf("path not cut from the left: %q", got)
	}

	// Multi-byte text is cut on characters.
	got = progressLine(runner.ProgressEvent{Tool: "WebSearch", Detail: strings.Repeat("ж", 100)})
	if !utf8.ValidString(got) || utf8.RuneCountInString(got) != progressLineMax {
		t.Errorf("cyrillic line = %q (%d chars)", got, utf8.RuneCountInString(got))
	}
}

func TestRedactSecrets(t *testing.T) {
	cases := []struct {
		in     string
		secret string // must not survive
		keep   string // must survive
	}{
		{`export ANTHROPIC_API_KEY=sk-ant-` + `abc123def456`, "sk-ant-" + "abc123def456", "ANTHROPIC_API_KEY="},
		{`TELEGRAM_BOT_TOKEN="12345:AAbbCC" ./gopod`, "12345:AAbbCC", "./gopod"},
		{`curl -H "Authorization: Bearer xyzSECRETxyz" https://api.x`, "xyzSECRETxyz", "https://api.x"},
		{`gh auth login --with-token ghp_` + `abcdefghijklmnop`, "ghp_" + "abcdefghijklmnop", "gh auth login"},
		{`mysql --password hunter22 -u root`, "hunter22", "-u root"},
		{`mysql --password=hunter22 db`, "hunter22", "db"},
		{`git clone https://bob:pa55word@github.com/x/y`, "pa55word", "github.com/x/y"},
		{`curl -d '{"api_key":"k-998877"}' http://h`, "k-998877", "http://h"},
		{`echo sk-proj-` + `AAAAAAAAAAAAAAAA`, "sk-proj-" + "AAAAAAAAAAAAAAAA", "echo"},
		{`aws s3 ls --profile x # AKIA` + `ABCDEFGHIJKLMNOP`, "AKIA" + "ABCDEFGHIJKLMNOP", "aws s3 ls"},
		{`curl -b "session_cookie=abc123" http://h`, "abc123", "http://h"},
	}
	for _, c := range cases {
		got := redactSecrets(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("%q -> %q: secret survived", c.in, got)
		}
		if !strings.Contains(got, c.keep) {
			t.Errorf("%q -> %q: lost %q", c.in, got, c.keep)
		}
	}
	for _, plain := range []string{"go test ./internal/auth/...", "ls -la /workspace/chat", "grep -rn token src/"} {
		if got := redactSecrets(plain); got != plain {
			t.Errorf("%q changed to %q", plain, got)
		}
	}
	// Redaction happens before the line is cut, so a secret cannot be
	// half-shown at the edge.
	line := progressLine(runner.ProgressEvent{Tool: "Bash", Detail: "PASSWORD=" + strings.Repeat("s", 80)})
	if strings.Contains(line, "sss") {
		t.Errorf("secret shown in progress line: %q", line)
	}
}

// Fix round 1: one case per bypass found in review.
func TestRedactSecretsBypasses(t *testing.T) {
	cases := []struct {
		name, in, secret, keep string
	}{
		{"bot token in URL path", `curl https://api.telegram.org/bot123456:AAHdqTcvCH1v` + `GWJxfSeofSAs0K5PALDsaw/getMe`, "AAHdqTcvCH1v" + "GWJxfSeofSAs0K5PALDsaw", "api.telegram.org/bot***"},
		{"curl -u", `curl -u alice:s3cretPW https://h/x`, "s3cretPW", "https://h/x"},
		{"curl --user", `curl --user alice:s3cretPW https://h/x`, "s3cretPW", "https://h/x"},
		{"curl --user=", `curl --user=alice:s3cretPW https://h/x`, "s3cretPW", "https://h/x"},
		{"slack webhook", `curl -X POST https://hooks.slack.com/services/T000AAA/B000BBB/XXXXsecretXXXX -d x`, "XXXXsecretXXXX", "hooks.slack.com/services/***"},
		{"mysql -p attached", `mysql -u root -phunter22 appdb`, "hunter22", "appdb"},
		{"sshpass -p", `sshpass -p hunter22 ssh host uptime`, "hunter22", "ssh host uptime"},
		{"escaped json", `curl -d "{\"token\":\"abc123xyz\"}" http://h`, "abc123xyz", "http://h"},
		{"aws configure set", `aws configure set aws_secret_access_key wJalrXUtn` + `FEMIK7MDENG`, "wJalrXUtn" + "FEMIK7MDENG", "aws configure set aws_secret_access_key"},
		{"export PW", `export PW=hunter2`, "hunter2", "export PW="},
		{"pass=", `run.sh pass=hunter2 --fast`, "hunter2", "--fast"},
		{"passwd flag", `useradd --passwd hunter2 bob`, "hunter2", "bob"},
		{"credential=", `tool credential=hunter2`, "hunter2", "tool"},
		{"gitlab token", `echo glpat-` + `abcdefghij1234567890`, "glpat-" + "abcdefghij1234567890", "echo"},
		{"google key", `echo AIza` + `SyA1234567890abcdefghijklmnopq`, "AIza" + "SyA1234567890abcdefghijklmnopq", "echo"},
		{"stripe key", `echo sk_live_` + `abcdefgh12345678`, "sk_live_" + "abcdefgh12345678", "echo"},
		{"presigned S3", `https://b.s3.amazonaws.com/k?X-Amz-Date=20261025&X-Amz-Signature=deadbeefcafe&x=1`, "deadbeefcafe", "X-Amz-Date=20261025"},
		{"sig=", `https://h/f?sig=deadbeefcafe&v=2`, "deadbeefcafe", "v=2"},
		{"signature=", `https://h/f?signature=deadbeefcafe`, "deadbeefcafe", "https://h/f?"},
	}
	for _, c := range cases {
		got := redactSecrets(c.in)
		if strings.Contains(got, c.secret) {
			t.Errorf("%s: %q -> %q: secret survived", c.name, c.in, got)
		}
		if !strings.Contains(got, c.keep) {
			t.Errorf("%s: %q -> %q: lost %q", c.name, c.in, got, c.keep)
		}
	}
	// Normal commands stay readable.
	for _, plain := range []string{
		"ls -p /workspace/chat",
		"go test -run Pass ./...",
		"find . -path ./vendor -prune",
		"gcc -pthread -o a a.c",
		"git push -u origin main",
		"grep -rn token src/",
		"psql -p 5432 -U app",
	} {
		if got := redactSecrets(plain); got != plain {
			t.Errorf("%q changed to %q", plain, got)
		}
	}
	// Only the first line of a command is ever shown.
	line := progressLine(runner.ProgressEvent{Tool: "Bash", Detail: "echo start\nexport X=1\necho visible-only-on-line-3"})
	if strings.Contains(line, "line-3") || strings.Contains(line, "X=1") {
		t.Errorf("progress line shows more than the first line: %q", line)
	}
}
