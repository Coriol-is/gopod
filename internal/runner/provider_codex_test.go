package runner

import "testing"

func TestCodexExtractLoginURL(t *testing.T) {
	p := NewCodexProvider("")
	const deviceURL = "https://auth.openai.com/codex/device"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"url line", "URL: " + deviceURL, deviceURL},
		{"url with trailing text", "URL: " + deviceURL + " (expires in 15m)", deviceURL},
		{"csi colored url", "URL: \x1b[1m" + deviceURL + "\x1b[0m", deviceURL},
		{
			"osc8 hyperlink",
			"\x1b]8;;" + deviceURL + "\x1b\\" + deviceURL + "\x1b]8;;\x1b\\",
			deviceURL,
		},
		{"device code", "ELJK-3JVGQ", "CODE:ELJK-3JVGQ"},
		{"no match", "Sign in with your browser", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := p.ExtractLoginURL(c.in)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestCodexVersionCmd(t *testing.T) {
	got := NewCodexProvider("").VersionCmd()
	want := []string{"codex", "--version"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("VersionCmd = %v, want %v", got, want)
	}
}

// codex-cli 0.160 removed --full-auto; the sandbox/approval bypass flag
// is accepted by both `exec` and `exec resume`. The container is the
// sandbox (ISOLATION.md), same stance as claude --dangerously-skip-permissions.
func TestCodexRunCmdFlags(t *testing.T) {
	p := NewCodexProvider("")
	for name, cmd := range map[string][]string{
		"RunCmd":      p.RunCmd("hello", ""),
		"RunFreshCmd": p.RunFreshCmd("hello"),
	} {
		joined := " " + join(cmd) + " "
		if contains(joined, " --full-auto ") {
			t.Errorf("%s still passes --full-auto (removed in codex 0.160): %v", name, cmd)
		}
		if !contains(joined, " --dangerously-bypass-approvals-and-sandbox ") {
			t.Errorf("%s missing --dangerously-bypass-approvals-and-sandbox: %v", name, cmd)
		}
		if !contains(joined, " --skip-git-repo-check ") {
			t.Errorf("%s missing --skip-git-repo-check: %v", name, cmd)
		}
		if cmd[len(cmd)-1] != "hello" {
			t.Errorf("%s prompt must be the last argument: %v", name, cmd)
		}
	}
	run := p.RunCmd("hello", "")
	if run[0] != "codex" || run[1] != "exec" || run[2] != "resume" || run[3] != "--last" {
		t.Errorf("RunCmd must start with codex exec resume --last: %v", run)
	}
	if got := p.RunCmd("q", "SYS"); got[len(got)-1] != "SYS\n\n---\n\nq" {
		t.Errorf("system prompt not prepended to prompt: %q", got[len(got)-1])
	}
}

func join(ss []string) string {
	out := ""
	for i, s := range ss {
		if i > 0 {
			out += " "
		}
		out += s
	}
	return out
}
