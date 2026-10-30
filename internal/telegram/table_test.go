package telegram

import (
	"fmt"
	"strings"
	"testing"
)

func TestTableCyrillicAlignedPre(t *testing.T) {
	md := "Итого:\n\n| Что | Сумма, RSD |\n|---|---:|\n| Аренда | 19 687,20 |\n| Кофе | 350 |\n\nГотово."
	want := "Итого:\n\n<pre>" +
		"Что    | Сумма, RSD\n" +
		"-------+-----------\n" +
		"Аренда |  19 687,20\n" +
		"Кофе   |        350</pre>\n\nГотово."
	for name, got := range map[string]string{"command": markdownToTelegramHTML(md), "final": finalReplyHTML(md)} {
		if got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, want)
		}
	}
}

func TestTableAlignmentAndCells(t *testing.T) {
	tests := []struct {
		name, md, want string
	}{
		{"center", "|a|b|\n|:-:|:-:|\n|xxxx|y|", "<pre> a   | b\n-----+--\nxxxx | y</pre>"},
		{"escaped pipe", `|a|b|` + "\n|-|-|\n" + `|x\|y|z|`, "<pre>a   | b\n----+--\nx|y | z</pre>"},
		{"pipe in code", "|a|b|\n|-|-|\n|`x|y`|z|", "<pre>a   | b\n----+--\nx|y | z</pre>"},
		{"inline markup stripped", "|**a**|b|\n|-|-|\n|[l](http://x.y)|*i* ~~s~~|", "<pre>a | b\n--+----\nl | i s</pre>"},
		{"escaped html", "|a|b|\n|-|-|\n|<x>|&|", "<pre>a   | b\n----+--\n&lt;x&gt; | &amp;</pre>"},
		{"ragged padded and cut", "|a|b|\n|-|-|\n|1|\n|1|2|3|", "<pre>a | b\n--+--\n1 |\n1 | 2</pre>"},
		{"no outer pipes", "a | b\n- | -\n1 | 2", "<pre>a | b\n--+--\n1 | 2</pre>"},
		{"cjk width 2", "|名|b|\n|-|-|\n|ab|c|", "<pre>名 | b\n---+--\nab | c</pre>"},
		{"emoji width 2", "|a|b|\n|-|-|\n|✅|c|", "<pre>a  | b\n---+--\n✅ | c</pre>"},
		{"header only", "|a|b|\n|-|-|", "<pre>a | b\n--+--</pre>"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestNotATable(t *testing.T) {
	for name, md := range map[string]string{
		"sentence with pipes": "use a | b | c to pipe",
		"single pipe line":    "| a | b |",
		"no separator":        "| a | b |\n| c | d |",
		"separator mismatch":  "| a | b |\n|---|\n| c | d |",
		"bad separator":       "| a | b |\n| x | y |\n| c | d |",
	} {
		if got := markdownToTelegramHTML(md); strings.Contains(got, "<pre>") {
			t.Errorf("%s became a table: %q", name, got)
		}
	}
}

func TestTableInsideFenceUntouched(t *testing.T) {
	md := "```\n| a | b |\n|---|---|\n| 1 | 2 |\n```"
	want := "<pre>| a | b |\n|---|---|\n| 1 | 2 |\n</pre>" // the fence renderer keeps the final newline
	if got := markdownToTelegramHTML(md); got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestTableWidthFallbackChoice(t *testing.T) {
	tests := []struct {
		name  string
		w     int // width of the first column; the second is 1 wide, joined by " | "
		rows  int
		cards bool
	}{
		{"exactly at the limit", tableMaxWidth - 4, 1, false},
		{"one over the limit", tableMaxWidth - 3, 1, true},
		{"over the limit without rows stays pre", tableMaxWidth - 3, 0, false},
	}
	for _, tc := range tests {
		header := []string{strings.Repeat("x", tc.w), "b"}
		var rows [][]string
		for i := 0; i < tc.rows; i++ {
			rows = append(rows, []string{"v", "w"})
		}
		got := renderTable(header, []tableAlign{alignLeft, alignLeft}, rows)
		if isCards := !strings.Contains(got, "<pre>"); isCards != tc.cards {
			t.Errorf("%s: cards=%v want %v: %q", tc.name, isCards, tc.cards, got)
		}
	}
}

func TestTableWideBecomesCards(t *testing.T) {
	md := "| Что | Сумма, RSD | Получатель / позив на број |\n|---|---:|---|\n| Аренда | 19 687,20 | ООО «Ромашка» / 97 123 |\n| Кофе | 350 | |"
	want := "<b>Что</b>: Аренда\n<b>Сумма, RSD</b>: 19 687,20\n<b>Получатель / позив на број</b>: ООО «Ромашка» / 97 123" +
		"\n\n<b>Что</b>: Кофе\n<b>Сумма, RSD</b>: 350"
	if got := finalReplyHTML(md); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestLongTableChunksStayValid(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("| Имя | Сумма |\n|:--|--:|\n")
	for i := 0; i < 400; i++ {
		sb.WriteString(fmt.Sprintf("| Платёж %d | %d,50 |\n", i, i*7))
	}
	md := strings.TrimSuffix(sb.String(), "\n")
	rendered := finalReplyHTML(md)
	chunks := chunkHTML(rendered, 4000)
	if len(chunks) < 2 {
		t.Fatalf("want several chunks, got %d", len(chunks))
	}
	checkChunks(t, rendered, chunks, 4000)
	for i, c := range chunks {
		if !strings.HasPrefix(c, "<pre>") || !strings.HasSuffix(c, "</pre>") {
			t.Errorf("chunk %d not wrapped in <pre>: %.40q ... %.40q", i, c, c[max(0, len(c)-40):])
		}
		for _, line := range strings.Split(strings.TrimSuffix(strings.TrimPrefix(c, "<pre>"), "</pre>"), "\n") {
			if i > 0 && strings.Count(line, "|") != 1 && line != "" {
				t.Errorf("chunk %d has a cut row: %q", i, line)
			}
		}
	}
	// Command paging: pages hold whole lines and every page renders.
	for i, p := range splitPages(md, pageFits) {
		if !pageFits(p) {
			t.Errorf("page %d too big", i)
		}
		if err := balanced(markdownToTelegramHTML(p)); err != nil {
			t.Errorf("page %d unbalanced: %v", i, err)
		}
	}
}
