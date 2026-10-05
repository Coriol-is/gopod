package runner

import "testing"

func TestFirstLine(t *testing.T) {
	cases := map[string]string{
		"2.1.289 (Claude Code)\n":         "2.1.289 (Claude Code)",
		"codex-cli 0.160.0\nextra line\n": "codex-cli 0.160.0",
		"\n\n  v1  \n":                    "v1",
		"":                                "",
	}
	for in, want := range cases {
		if got := firstLine(in); got != want {
			t.Errorf("firstLine(%q) = %q, want %q", in, got, want)
		}
	}
}
