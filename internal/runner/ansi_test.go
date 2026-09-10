package runner

import "testing"

func TestStripTerminalEscapes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "hello", "hello"},
		{"csi color", "\x1b[94mblue\x1b[39m", "blue"},
		{"csi cursor", "\x1b[2Ktext", "text"},
		{"osc bel terminated", "\x1b]0;title\x07text", "text"},
		{"osc st terminated", "\x1b]8;;https://x.com\x1b\\link", "link"},
		{"osc8 hyperlink wrap", "\x1b]8;;https://x.com/a\x1b\\visible\x1b]8;;\x1b\\", "visible"},
		{"unterminated csi", "\x1b[94", ""},
		{"unterminated osc", "\x1b]8;;https://x.com", ""},
		{"mixed", "a\x1b[1mb\x1b]0;t\x07c", "abc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := stripTerminalEscapes(c.in)
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
