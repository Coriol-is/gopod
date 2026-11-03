package control

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestTruncateCommandTextAtRuneBoundary(t *testing.T) {
	got := truncateStr("😀привет", 3)
	if got != "😀пр…" || !utf8.ValidString(got) {
		t.Fatal(got)
	}
}
func TestCommandValueCannotCloseInlineCode(t *testing.T) {
	got := safeCommandValue("`[admin](https://evil.invalid)\n```hidden")
	if strings.Count(got, "`") != 2 || strings.ContainsAny(got, "\r\n") {
		t.Fatal(got)
	}
}

func TestProviderFallbackDeniesMemberBeforeDependencies(t *testing.T) {
	r := New(nil)
	RegisterProviderFallbackCommand(r, nil, nil, nil)
	resp, err := r.Dispatch(t.Context(), Command{Name: "provider.fallback", Confirmed: true, Args: []string{"1", "claude", "codex"}, Caller: Caller{ChatID: -200, Source: SourceTelegram}})
	if err != nil || resp.Code != 1 {
		t.Fatalf("member fallback not refused: %+v %v", resp, err)
	}
}
