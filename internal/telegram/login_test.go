package telegram

import "testing"

// A device-auth login (codex) never takes a pasted code, so the next
// ordinary message must reach the agent instead of the login's stdin.
// Before the fix, the first "ping" after /login was swallowed.
func TestLoginSessionAwaitingCode(t *testing.T) {
	const chat = int64(42)
	cases := []struct {
		name    string
		session *loginSession
		text    string
		want    bool
	}{
		{"no session", nil, "ping", false},
		{"oauth code flow takes the next message", &loginSession{awaitsCode: true}, "abc#def", true},
		{"slash commands are never intercepted", &loginSession{awaitsCode: true}, "/help", false},
		{"device flow does not intercept", &loginSession{awaitsCode: false}, "ping", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ls := newLoginSessions()
			if c.session != nil {
				ls.set(chat, c.session)
			}
			got := ls.awaitingCode(chat, c.text)
			if (got != nil) != c.want {
				t.Errorf("awaitingCode(%q) = %v, want intercept=%v", c.text, got, c.want)
			}
		})
	}
}
