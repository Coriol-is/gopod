package telegram

import (
	"fmt"
	"regexp"
	"strconv"
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
		{"inline markup stripped", "|**a**|b|\n|-|-|\n|[l](http://x.y)|*i* ~~s~~|", "<pre>a              | b\n---------------+----\nl (http://x.y) | i s</pre>"},
		{"escaped html", "|a|b|\n|-|-|\n|<x>|&|", "<pre>a   | b\n----+--\n&lt;x&gt; | &amp;</pre>"},
		{"ragged padded", "|a|b|\n|-|-|\n|1|\n|1|2|", "<pre>a | b\n--+--\n1 |\n1 | 2</pre>"},
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
		"pipe then rule":      "Done |\n---",
		"escapes still apply": "a < b | c & d",
	} {
		want := strings.NewReplacer("&", "&amp;", "<", "&lt;").Replace(md)
		if got := markdownToTelegramHTML(md); got != want {
			t.Errorf("%s:\ngot  %q\nwant %q", name, got, want)
		}
	}
}

func TestTableMoreCases(t *testing.T) {
	long := strings.Repeat("x", 40)
	tests := []struct{ name, md, want string }{
		{"escaped pipe in code span", "|a|b|\n|-|-|\n|`x\\|y`|z|", "<pre>a   | b\n----+--\nx|y | z</pre>"},
		{"code contents verbatim", "|a|b|\n|-|-|\n|`a*b*c`|`__init__.py`|",
			"<pre>a     | b\n------+------------\na*b*c | __init__.py</pre>"},
		{"overflow row ends the table", "|a|\n|-|\n|x|\nNote: a | b", "<pre>a\n-\nx</pre>\nNote: a | b"},
		{"overflow row is not swallowed", "|a|\n|-|\n|x|y|", "<pre>a\n-</pre>\n|x|y|"},
		{"escaped header in pre", "|<a>|&b|\n|-|-|\n|1|2|", "<pre>&lt;a&gt; | &amp;b\n----+---\n1   | 2</pre>"},
		{"escaped header in cards", "|<h>|&" + long + "|\n|-|-|\n|v|w|", "<b>v</b>\n&amp;" + long + " w"},
		{"after a paragraph", "Intro line\n| a | b |\n|---|---|\n| 1 | 2 |", "Intro line\n<pre>a | b\n--+--\n1 | 2</pre>"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestTableReviewRound2(t *testing.T) {
	long := strings.Repeat("x", 40)
	tests := []struct{ name, md, want string }{
		{"emphasis around code", "|a|b|\n|-|-|\n|**a `b` c**|[`x`](http://u)|", "<pre>a     | b\n------+-------------\na b c | x (http://u)</pre>"},
		{"unescaped pipe in code splits", "|a|b|\n|-|-|\n|`x|y`|z|", "<pre>a | b\n--+--</pre>\n|<code>x|y</code>|z|"},
		{"unmatched backticks across cells", "|a|b|c|\n|-|-|-|\n|5`|x`y|z|", "<pre>a  | b   | c\n---+-----+--\n5` | x`y | z</pre>"},
		{"double backtick span", "|a|b|\n|-|-|\n|``a`b``|z|", "<pre>a   | b\n----+--\na`b | z</pre>"},
		{"quote after table", "|a|b|\n|-|-|\n|1|2|\n> note | x", "<pre>a | b\n--+--\n1 | 2</pre>\n<blockquote>note | x</blockquote>"},
		{"heading after table", "|a|b|\n|-|-|\n|1|2|\n## Итог | x", "<pre>a | b\n--+--\n1 | 2</pre>\n<b>Итог | x</b>"},
		{"bullet after table", "|a|b|\n|-|-|\n|1|2|\n- a | b", "<pre>a | b\n--+--\n1 | 2</pre>\n• a | b"},
		{"numbered after table", "|a|b|\n|-|-|\n|1|2|\n1. a | b", "<pre>a | b\n--+--\n1 | 2</pre>\n1. a | b"},
		{"trailing empty cell", "|a|b|\n|-|-|\n|1|2| |", "<pre>a | b\n--+--\n1 | 2</pre>"},
		{"overflow line is not a header", "|a|\n|-|\n|x|y|\n|-|-|\n|1|2|", "<pre>a\n-</pre>\n|x|y|\n|-|-|\n|1|2|"},
		{"tab expanded", "|a|\n|-|\n|x\ty|", "<pre>a\n------\nx    y</pre>"},
		{"whitespace header cards", "|__ __|" + long + "|\n|-|-|\n|v|w|", "<b>v</b>\n" + long + " w"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestDisplayWidthRound2(t *testing.T) {
	for name, tc := range map[string]struct {
		s    string
		want int
	}{
		"zwj between letters": {"a\u200Db", 2},
		"vs16 on a letter":    {"a\uFE0F", 1},
		"zwj after emoji":     {"\U0001F468\u200D\U0001F469", 2},
	} {
		if got := displayWidth(tc.s); got != tc.want {
			t.Errorf("%s: width %d want %d", name, got, tc.want)
		}
	}
}

func TestTableCardsEmptyHeader(t *testing.T) {
	got := renderTable([]string{"", strings.Repeat("x", 40)}, []tableAlign{alignLeft, alignLeft}, [][]string{{"v", "w"}})
	if want := "<b>v</b>\n" + strings.Repeat("x", 40) + " w"; got != want {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestDisplayWidth(t *testing.T) {
	for name, tc := range map[string]struct {
		s    string
		want int
	}{
		"latin":         {"abc", 3},
		"cyrillic":      {"Что", 3},
		"cjk":           {"名前", 4},
		"check mark":    {"\u2705", 2},
		"green circle":  {"\U0001F7E2", 2},
		"alarm clock":   {"\u23F0", 2},
		"hourglass":     {"\u23F3", 2},
		"squared CL":    {"\U0001F191", 2},
		"skin tone":     {"\U0001F44D\U0001F3FD", 2},
		"zwj family":    {"\U0001F468\u200D\U0001F469\u200D\U0001F467", 2},
		"heart vs16":    {"\u2764\uFE0F", 2},
		"heart text":    {"\u2764", 1},
		"combining":     {"e\u0301", 1},
		"emoji in text": {"a\U0001F44Db", 4},
	} {
		if got := displayWidth(tc.s); got != tc.want {
			t.Errorf("%s: width %d want %d", name, got, tc.want)
		}
	}
}

func TestTableThroughPrepareIPCText(t *testing.T) {
	parts, err := PrepareIPCText("| a | b |\n|---|---|\n| 1 | 2 |", "markdown")
	if err != nil || len(parts) != 1 {
		t.Fatalf("parts %d err %v", len(parts), err)
	}
	if got, want := string(parts[0].Payload), "<pre>a | b\n--+--\n1 | 2</pre>"; got != want {
		t.Errorf("got %q want %q", got, want)
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
	// The first column is w wide, the second 1, joined by " | ": total w+4.
	for _, tc := range []struct {
		total int
		cards bool
	}{{37, false}, {38, false}, {39, true}, {60, true}} {
		if tc.total == 39 {
			if got, want := markdownToTelegramHTML("|"+strings.Repeat("x", 35)+"|b|\n|-|-|\n|v|w|"), "<b>v</b>\nb w"; got != want {
				t.Errorf("width 39: got %q want %q", got, want)
			}
		}
		md := "|" + strings.Repeat("x", tc.total-4) + "|b|\n|-|-|\n|v|w|"
		got := markdownToTelegramHTML(md)
		if isCards := !strings.Contains(got, "<pre>"); isCards != tc.cards {
			t.Errorf("width %d: cards=%v want %v: %q", tc.total, isCards, tc.cards, got)
		}
	}
	if got := markdownToTelegramHTML("|" + strings.Repeat("x", 56) + "|b|\n|-|-|"); !strings.HasPrefix(got, "<pre>") {
		t.Errorf("wide header-only table should stay <pre>: %q", got)
	}
}

func TestTableWideBecomesCards(t *testing.T) {
	md := "| Что | Сумма, RSD | Получатель / позив на број |\n|---|---:|---|\n| Аренда | 19 687,20 | ООО «Ромашка» / 97 123 |\n| Кофе | 350 | |"
	want := "<b>Аренда</b>\nСумма, RSD 19 687,20 · Получатель / позив на број ООО «Ромашка» / 97 123" +
		"\n\n<b>Кофе</b>\nСумма, RSD 350"
	if got := finalReplyHTML(md); got != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

const ownerTable = `| Список | Голоса | % | Было 26.10 | Мандаты |
|---|---:|---:|---:|---:|
| Студенческий список | 2 349 689 | 53,04 | 52,54 | 141 |
| Список «Единство» | 1 012 345 | 22,85 | 23,10 | 60 |
| Зелёные | 410 000 | 9,26 | 8,90 | 24 |
| Левый блок | 250 111 | 5,65 | 6,01 | 15 |
| Независимые | 150 000 | 3,39 | 3,12 | 9 |
| Малые партии | 120 000 | <1 | — | 0 |
| Прочие | 80 000 | 1,81 | 2,00 | 0 |
| Против всех | 55 321 | 1,25 | 1,30 | 0 |
| Итого | 4 427 466 | 100 | | 249 |`

func TestTableCompactCardsOwnerExample(t *testing.T) {
	want := `<b>Студенческий список</b>
Голоса 2 349 689 · % 53,04 · Было 26.10 52,54 · Мандаты 141

<b>Список «Единство»</b>
Голоса 1 012 345 · % 22,85 · Было 26.10 23,10 · Мандаты 60

<b>Зелёные</b>
Голоса 410 000 · % 9,26 · Было 26.10 8,90 · Мандаты 24

<b>Левый блок</b>
Голоса 250 111 · % 5,65 · Было 26.10 6,01 · Мандаты 15

<b>Независимые</b>
Голоса 150 000 · % 3,39 · Было 26.10 3,12 · Мандаты 9

<b>Малые партии</b>
Голоса 120 000 · % &lt;1 · Было 26.10 — · Мандаты 0

<b>Прочие</b>
Голоса 80 000 · % 1,81 · Было 26.10 2,00 · Мандаты 0

<b>Против всех</b>
Голоса 55 321 · % 1,25 · Было 26.10 1,30 · Мандаты 0

<b>Итого</b>
Голоса 4 427 466 · % 100 · Мандаты 249`
	if got := finalReplyHTML(ownerTable); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestTableCompactCardRules(t *testing.T) {
	long := strings.Repeat("h", 40)
	hdr := "| A | B | C | " + long + " |\n|-|-|-|-|\n"
	tests := []struct{ name, md, want string }{
		{"empty first cell uses first non-empty", hdr + "| | t | 6 | |", "<b>t</b>\nC 6"},
		{"title only", hdr + "| T | | | |", "<b>T</b>"},
		{"all-empty row dropped", hdr + "| T | 1 | | |\n| | | | |\n| U | 2 | | |", "<b>T</b>\nB 1\n\n<b>U</b>\nB 2"},
		{"dash and escaped kept", hdr + "| T | — | <1 | a&b |", "<b>T</b>\nB — · C &lt;1 · " + long + " a&amp;b"},
		{"title escaped", hdr + "| <T> | 1 | | |", "<b>&lt;T&gt;</b>\nB 1"},
		{"empty header gives bare value", "| A | | C | " + long + " |\n|-|-|-|-|\n| T | 1 | 2 | |", "<b>T</b>\n1 · C 2"},
		{"code span and link", hdr + "| T | `x_y` | [l](http://u) | |", "<b>T</b>\nB x_y · C l (http://u)"},
		{"two columns", "| " + long + " | B |\n|-|-|\n| T | 1 |", "<b>T</b>\nB 1"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestTableWideOneColumnOrNoHeadersKeepsSimpleForm(t *testing.T) {
	long := strings.Repeat("h", 40)
	tests := []struct{ name, md, want string }{
		{"one column", "| " + long + " |\n|-|\n| a |\n| b |", "<b>" + long + "</b>: a\n\n<b>" + long + "</b>: b"},
		{"no headers", "| | |\n|-|-|\n| " + long + " | y |\n| b | |", long + "\ny\n\nb"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestCompactCardsChunkAndIPC(t *testing.T) {
	var sb strings.Builder
	sb.WriteString(strings.Split(ownerTable, "\n")[0] + "\n" + strings.Split(ownerTable, "\n")[1] + "\n")
	for i := 0; i < 300; i++ {
		fmt.Fprintf(&sb, "| Список %d | %d 000 | %d,5 | — | %d |\n", i, i, i%100, i%9)
	}
	md := strings.TrimSuffix(sb.String(), "\n")
	rendered := finalReplyHTML(md)
	chunks := chunkHTML(rendered, 4000)
	if len(chunks) < 2 {
		t.Fatalf("want several chunks, got %d", len(chunks))
	}
	checkChunks(t, rendered, chunks, 4000)
	parts, err := PrepareIPCText(ownerTable, "markdown")
	if err != nil || len(parts) != 1 {
		t.Fatalf("parts %d err %v", len(parts), err)
	}
	if got := string(parts[0].Payload); got != finalReplyHTML(ownerTable) {
		t.Errorf("IPC payload differs from the rendered cards: %q", got)
	}
}

// A one-character header such as "%" is emitted as a well-formed <b>%</b>
// and survives the sanitizer; it only looks unbolded because the glyph is thin.
func TestBoldPercentSurvivesSanitizer(t *testing.T) {
	got, ok := sanitizeTelegramHTML("<b>%</b>: 53")
	if !ok || got != "<b>%</b>: 53" {
		t.Errorf("got %q ok=%v", got, ok)
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
	rowRe := regexp.MustCompile(`^Платёж (\d+) +\| +(\d+),50$`)
	next := 0
	for i, c := range chunks {
		if !strings.HasPrefix(c, "<pre>") || !strings.HasSuffix(c, "</pre>") {
			t.Errorf("chunk %d not wrapped in <pre>", i)
		}
		lines := strings.Split(strings.TrimSuffix(strings.TrimPrefix(c, "<pre>"), "</pre>"), "\n")
		if i == 0 {
			lines = lines[2:] // header and rule
		}
		for _, line := range lines {
			if line == "" { // a cut after a newline leaves one before </pre>
				continue
			}
			m := rowRe.FindStringSubmatch(line)
			if m == nil {
				t.Fatalf("chunk %d has a malformed or cut row: %q", i, line)
			}
			if m[1] != strconv.Itoa(next) || m[2] != strconv.Itoa(next*7) {
				t.Fatalf("row out of order or altered: %q, want index %d", line, next)
			}
			next++
		}
	}
	if next != 400 {
		t.Errorf("got %d rows across chunks, want 400", next)
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

func TestTableCellNULAndLinks(t *testing.T) {
	long := strings.Repeat("x", 40)
	tests := []struct{ name, md, want string }{
		{"nul next to code", "|a|b|\n|-|-|\n|\x000\x00 `c`|z|", "<pre>a   | b\n----+--\n0 c | z</pre>"},
		{"link keeps url in pre", "|a|b|\n|-|-|\n|[t](http://u/?a=1&b=2)|z|", "<pre>a                     | b\n----------------------+--\nt (http://u/?a=1&amp;b=2) | z</pre>"},
		{"link keeps url in cards", "|a|" + long + "|\n|-|-|\n|[t](http://u)|z|", "<b>t (http://u)</b>\n" + long + " z"},
		{"empty-ish link text", "|a|b|\n|-|-|\n|[ ](http://u)|z|", "<pre>a          | b\n-----------+--\n(http://u) | z</pre>"},
	}
	for _, tc := range tests {
		got := markdownToTelegramHTML(tc.md)
		if got != tc.want || strings.ContainsRune(got, 0) {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestCompactCardsReview(t *testing.T) {
	long := strings.Repeat("h", 40)
	hdr := "| A | B | C | " + long + " |\n|-|-|-|-|\n"
	tests := []struct{ name, md, want string }{
		{"all-empty rows keep the table", "|A|" + long + "|\n|-|-|\n| | |\n| | |",
			"<pre>A | " + long + "\n--+-" + strings.Repeat("-", 40) + "\n  |\n  |</pre>"},
		{"one column empty row skipped", "| " + long + " |\n|-|\n| x |\n| |\n| y |",
			"<b>" + long + "</b>: x\n\n<b>" + long + "</b>: y"},
		{"whitespace title falls to next cell", hdr + "| ** ** | 1 | | |", "<b>1</b>"},
		{"whitespace code title", hdr + "| ` ` | 1 | 2 | |", "<b>1</b>\nC 2"},
		{"duplicate headers", "| A | A | " + long + " |\n|-|-|-|\n| t | 1 | 2 |", "<b>t</b>\nA 1 · " + long + " 2"},
	}
	for _, tc := range tests {
		if got := markdownToTelegramHTML(tc.md); got != tc.want {
			t.Errorf("%s:\ngot  %q\nwant %q", tc.name, got, tc.want)
		}
	}
}

func TestCompactCards500RowsChunk(t *testing.T) {
	var sb strings.Builder
	sb.WriteString("| Список | Голоса | % | Мандаты | Итог |\n|-|-|-|-|-|\n")
	for i := 0; i < 500; i++ {
		fmt.Fprintf(&sb, "| Список %d | %d 000 | %d,5 | %d | — |\n", i, i, i%100, i%9)
	}
	rendered := finalReplyHTML(strings.TrimSuffix(sb.String(), "\n"))
	chunks := chunkHTML(rendered, 4000)
	checkChunks(t, rendered, chunks, 4000)
	if got := strings.Count(strings.Join(chunks, ""), "<b>Список "); got != 500 {
		t.Errorf("titles across chunks: %d, want 500", got)
	}
}
