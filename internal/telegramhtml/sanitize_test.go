package telegramhtml

import (
	"strings"
	"testing"
)

func TestSanitizeCanonicalAndHostileInputs(t *testing.T) {
	for _, s := range []string{`<b>x</b>`, `<a href="https://example.com/?a=1&amp;b=2">safe</a>`, `<span class="tg-spoiler" ignored="x">x</span>`, `<strong>safe</strong>`, `<x>literal</x>`} {
		clean, ok := Sanitize(s)
		if !ok || !Valid(clean) {
			t.Errorf("%q => %q %v", s, clean, ok)
		}
	}
	for _, s := range []string{`<a href="https://example.com/` + strings.Repeat("x", 2048) + `">label</a>`, `<b><i>x</b></i>`, `<code><b>x</b></code>`, `<blockquote><blockquote>x</blockquote></blockquote>`, strings.Repeat("<b>", 33) + "x" + strings.Repeat("</b>", 33)} {
		if _, ok := Sanitize(s); ok {
			t.Errorf("accepted %q", s)
		}
	}
	for _, u := range []string{"javascript:alert(1)", "data:text/html,bad", "file:///etc/passwd", "https://", "https:\\bad"} {
		clean, ok := Sanitize(`<a href="` + u + `">label</a>`)
		if !ok || clean != "label" {
			t.Errorf("unsafe URL %q => %q %v", u, clean, ok)
		}
	}
	if Valid(`<b onclick="bad">x</b>`) || Valid(`<script>x</script>`) {
		t.Fatal("store must reject noncanonical markup")
	}
}
func FuzzSanitize(f *testing.F) {
	for _, s := range []string{"hello", `<b>x</b>`, `<blockquote expandable>x</blockquote>`, `<script>x</script>`, "<b>"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if len(s) > 65536 {
			t.Skip()
		}
		if clean, ok := Sanitize(s); ok && !Valid(clean) {
			t.Fatalf("non-idempotent %q => %q", s, clean)
		}
	})
}
