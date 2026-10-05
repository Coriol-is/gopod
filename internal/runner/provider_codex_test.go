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
