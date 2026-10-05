package runner

import "testing"

func TestClaudeExtractLoginURL(t *testing.T) {
	p := NewClaudeProvider("")
	const oauthURL = "https://claude.ai/oauth/authorize?client_id=abc_def&response_type=code&code_challenge=x_y"
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"visit prefix", "If the browser didn't open, visit: " + oauthURL, oauthURL},
		{"bare url", oauthURL, oauthURL},
		{"url with trailing text", oauthURL + " (opens in browser)", oauthURL},
		{
			// claude CLI >= 2.1.267 wraps the URL in an OSC-8 hyperlink:
			// ESC]8;;URL ESC\ [94mURL[39m ESC]8;; ESC\ — the escape params
			// duplicate the visible URL.
			"osc8 hyperlink",
			"\x1b]8;;" + oauthURL + "\x1b\\\x1b[94m" + oauthURL + "\x1b[39m\x1b]8;;\x1b\\",
			oauthURL,
		},
		{"no url", "some other line", ""},
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

func TestClaudeVersionCmd(t *testing.T) {
	got := NewClaudeProvider("").VersionCmd()
	want := []string{"claude", "--version"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("VersionCmd = %v, want %v", got, want)
	}
}
