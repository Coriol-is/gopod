package telegram

import (
	"fmt"
	"strings"
	"testing"
)

// fence returns a fenced markdown block of n numbered lines.
func fence(lang string, n int) string {
	var sb strings.Builder
	sb.WriteString("```" + lang + "\n")
	for i := 1; i <= n; i++ {
		fmt.Fprintf(&sb, "line %d <x>\n", i)
	}
	sb.WriteString("```")
	return sb.String()
}

func TestFinalReplyHTML_ShortBlockUnchanged(t *testing.T) {
	md := "Fix:\n" + fence("go", collapseOverLines) + "\nok"
	if got, want := finalReplyHTML(md), markdownToTelegramHTML(md); got != want {
		t.Errorf("12-line block changed\ngot:  %q\nwant: %q", got, want)
	}
	if strings.Contains(finalReplyHTML(md), "blockquote") {
		t.Error("12-line block collapsed")
	}
}

func TestFinalReplyHTML_LongBlockCollapsed(t *testing.T) {
	md := "Fix:\n" + fence("go", collapseOverLines+1) + "\nok"
	got := finalReplyHTML(md)
	want := "Fix:\n<blockquote expandable><pre>line 1 &lt;x&gt;\n"
	if !strings.HasPrefix(got, want) {
		t.Errorf("got %q, want prefix %q", got, want)
	}
	if !strings.HasSuffix(got, "line 13 &lt;x&gt;\n</pre></blockquote>\nok") {
		t.Errorf("got %q, want the block closed inside the quote, then the text", got)
	}
	// Command replies never collapse.
	if strings.Contains(markdownToTelegramHTML(md), "blockquote") {
		t.Error("markdownToTelegramHTML collapsed a block")
	}
}

func TestFinalReplyHTML_UnclosedLongBlock(t *testing.T) {
	// A stopped turn can end inside a fence.
	md := "partial\n```py\n" + strings.Repeat("x = 1\n", 20)
	got := finalReplyHTML(md)
	if !strings.HasSuffix(got, "</pre></blockquote>") || balanced(got) != nil {
		t.Errorf("unclosed fence not closed: %q", got)
	}
}
