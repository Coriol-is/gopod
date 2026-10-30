package telegram

import (
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// tableMaxWidth is the widest aligned monospace table, in display columns,
// that renderTable draws as a <pre> block (about a phone screen). Wider
// tables fall back to row cards.
const tableMaxWidth = 38

type tableAlign int

const (
	alignLeft tableAlign = iota
	alignRight
	alignCenter
)

var tableSepCellRe = regexp.MustCompile(`^:?-+:?$`)

// parseTable reads a GFM table starting at lines[start]: a header line
// with a pipe, a separator line with as many dash cells, then zero or more
// pipe rows. It returns the header, alignments, data rows (each padded to the
// header width; a line with more cells ends the table) and the index of the last line consumed.
func parseTable(lines []string, start int) (header []string, aligns []tableAlign, rows [][]string, end int, ok bool) {
	if start+1 >= len(lines) || !strings.Contains(lines[start], "|") || isFence(lines[start]) {
		return nil, nil, nil, 0, false
	}
	header = splitTableRow(lines[start])
	sep := splitTableRow(lines[start+1])
	if !strings.Contains(lines[start+1], "|") || len(header) == 0 || len(sep) != len(header) {
		return nil, nil, nil, 0, false
	}
	for _, c := range sep {
		if !tableSepCellRe.MatchString(c) {
			return nil, nil, nil, 0, false
		}
		a := alignLeft
		switch {
		case strings.HasPrefix(c, ":") && strings.HasSuffix(c, ":"):
			a = alignCenter
		case strings.HasSuffix(c, ":"):
			a = alignRight
		}
		aligns = append(aligns, a)
	}
	end = start + 1
	for end+1 < len(lines) && strings.Contains(lines[end+1], "|") && !isFence(lines[end+1]) && !startsBlock(lines[end+1]) {
		row := splitTableRow(lines[end+1])
		for len(row) > len(header) && row[len(row)-1] == "" {
			row = row[:len(row)-1] // trailing empty cells are padding
		}
		if len(row) > len(header) {
			// More cells than the header: not a row of this table. Leave
			// the line as text rather than drop its extra cells.
			break
		}
		end++
		for len(row) < len(header) {
			row = append(row, "")
		}
		rows = append(rows, row)
	}
	return header, aligns, rows, end, true
}

var orderedItemRe = regexp.MustCompile(`^\d+[.)] `)

// startsBlock reports whether line opens a quote, heading or list item,
// which ends a table even when it contains a pipe.
func startsBlock(line string) bool {
	t := strings.TrimSpace(line)
	if strings.HasPrefix(t, ">") || orderedItemRe.MatchString(t) {
		return true
	}
	if _, ok := markdownHeading(t); ok {
		return true
	}
	return strings.HasPrefix(t, "- ") || strings.HasPrefix(t, "* ") || strings.HasPrefix(t, "+ ")
}

// splitTableRow splits a table line into trimmed cells, following GFM: an
// unescaped pipe always splits (even inside a code span), and \| is a
// literal pipe.
func splitTableRow(line string) []string {
	line = strings.TrimSpace(line)
	var cells []string
	var cur strings.Builder
	for i := 0; i < len(line); {
		switch c := line[i]; {
		case c == '\\' && i+1 < len(line) && line[i+1] == '|':
			cur.WriteByte('|')
			i += 2
		case c == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
			i++
		default:
			cur.WriteByte(c)
			i++
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	// Leading and trailing pipes produce empty edge cells.
	if strings.HasPrefix(line, "|") {
		cells = cells[1:]
	}
	if len(cells) > 0 && strings.HasSuffix(line, "|") && !strings.HasSuffix(line, `\|`) {
		cells = cells[:len(cells)-1]
	}
	return cells
}

var (
	cellLinkRe   = regexp.MustCompile(`\[([^\]\n]+)\]\([^)\s]+\)`)
	cellBoldRe   = regexp.MustCompile(`\*\*(.+?)\*\*|__(.+?)__`)
	cellStrikeRe = regexp.MustCompile(`~~(.+?)~~`)
	cellItalicRe = regexp.MustCompile(`\*(\S[^*]*?)\*`)
)

// plainCell strips inline markdown from a cell, leaving plain text. Code
// spans are swapped for placeholders while the emphasis and link patterns
// run, so they can wrap a span without touching its contents.
func plainCell(s string) string {
	var codes []string
	var masked strings.Builder
	for i := 0; i < len(s); {
		if s[i] != '`' {
			masked.WriteByte(s[i])
			i++
			continue
		}
		n := 1
		for i+n < len(s) && s[i+n] == '`' {
			n++
		}
		if end := closingRun(s, i+n, n); end >= 0 {
			masked.WriteString("\x00" + strconv.Itoa(len(codes)) + "\x00")
			codes = append(codes, s[i+n:end])
			i = end + n
			continue
		}
		masked.WriteString(s[i : i+n]) // unmatched: literal backticks
		i += n
	}
	out := plainEmphasis(masked.String())
	for k, c := range codes {
		out = strings.Replace(out, "\x00"+strconv.Itoa(k)+"\x00", c, 1)
	}
	return strings.ReplaceAll(out, "\t", "    ")
}

// closingRun returns the index of the next run of exactly n backticks in
// s at or after from, or -1.
func closingRun(s string, from, n int) int {
	for i := from; i < len(s); {
		if s[i] != '`' {
			i++
			continue
		}
		j := i
		for j < len(s) && s[j] == '`' {
			j++
		}
		if j-i == n {
			return i
		}
		i = j
	}
	return -1
}

func plainEmphasis(s string) string {
	s = cellLinkRe.ReplaceAllString(s, "$1")
	s = cellBoldRe.ReplaceAllString(s, "$1$2")
	s = cellStrikeRe.ReplaceAllString(s, "$1")
	return cellItalicRe.ReplaceAllString(s, "$1")
}

// displayWidth is the terminal column width of s: CJK and emoji count 2,
// combining marks, joiners and variation selectors 0, the rest 1.
func displayWidth(s string) int {
	w, last := 0, 0 // last is the width of the most recent glyph
	joined := false // the previous rune was an emoji-joining ZWJ
	var prev rune
	for _, r := range s {
		switch {
		case r == 0x200D:
			joined = last == 2
		case r == 0xFE0F: // emoji presentation: a narrow emoji-capable base becomes wide
			if last == 1 && emojiCapable(prev) {
				w++
				last = 2
			}
		case r >= 0x1F3FB && r <= 0x1F3FF, // skin-tone modifiers
			unicode.Is(unicode.Mn, r):
		case joined:
			joined = false
		default:
			last = runeWidth(r)
			w += last
			prev = r
		}
	}
	return w
}

// emojiCapable reports whether r can take an emoji presentation selector.
func emojiCapable(r rune) bool {
	switch r {
	case 0xA9, 0xAE, 0x203C, 0x2049, 0x2122, 0x2139, 0x3030, 0x303D, 0x3297, 0x3299:
		return true
	}
	return (r >= 0x2190 && r <= 0x2BFF) || (r >= 0x1F000 && r <= 0x1FAFF)
}

func runeWidth(r rune) int {
	switch {
	case r >= 0x1100 && r <= 0x115F,
		r >= 0x2E80 && r <= 0xA4CF,
		r >= 0xAC00 && r <= 0xD7A3,
		r >= 0xF900 && r <= 0xFAFF,
		r >= 0xFE30 && r <= 0xFE6F,
		r >= 0xFF00 && r <= 0xFF60,
		r >= 0xFFE0 && r <= 0xFFE6,
		r >= 0x1F300 && r <= 0x1F64F,
		r >= 0x1F680 && r <= 0x1F6FF,
		r >= 0x1F900 && r <= 0x1F9FF,
		r >= 0x1F7E0 && r <= 0x1F7EB,
		r >= 0x1F191 && r <= 0x1F19A,
		r >= 0x1FA70 && r <= 0x1FAFF,
		r >= 0x20000 && r <= 0x3FFFD:
		return 2
	}
	if wideSymbols[r] {
		return 2
	}
	return 1
}

// wideSymbols are BMP emoji presented two columns wide.
var wideSymbols = func() map[rune]bool {
	m := map[rune]bool{}
	for _, r := range []rune{0x231A, 0x231B, 0x23F0, 0x23F3, 0x2614, 0x2615, 0x267F, 0x2693, 0x26A1, 0x26CE, 0x26D4, 0x26EA,
		0x26F5, 0x26FA, 0x26FD, 0x2705, 0x2728, 0x274C, 0x274E, 0x2757, 0x27B0, 0x27BF, 0x2B50, 0x2B55} {
		m[r] = true
	}
	for _, span := range [][2]rune{{0x23E9, 0x23EC}, {0x2648, 0x2653}, {0x26AA, 0x26AB}, {0x26BD, 0x26BE},
		{0x26C4, 0x26C5}, {0x26F2, 0x26F3}, {0x270A, 0x270B}, {0x2753, 0x2755}, {0x2795, 0x2797}, {0x2B1B, 0x2B1C}} {
		for r := span[0]; r <= span[1]; r++ {
			m[r] = true
		}
	}
	return m
}()

func padCell(s string, width int, a tableAlign) string {
	gap := width - displayWidth(s)
	if gap <= 0 {
		return s
	}
	switch a {
	case alignRight:
		return strings.Repeat(" ", gap) + s
	case alignCenter:
		return strings.Repeat(" ", gap/2) + s + strings.Repeat(" ", gap-gap/2)
	}
	return s + strings.Repeat(" ", gap)
}

// renderTable draws a parsed table as Telegram HTML: an aligned <pre>
// block when it is at most tableMaxWidth columns wide, otherwise (and when
// it has data rows) one card per row of "Header: value" lines.
func renderTable(header []string, aligns []tableAlign, rows [][]string) string {
	plainHeader := make([]string, len(header))
	widths := make([]int, len(header))
	for c, h := range header {
		plainHeader[c] = strings.TrimSpace(plainCell(h))
		widths[c] = max(displayWidth(plainHeader[c]), 1)
	}
	plainRows := make([][]string, len(rows))
	for r, row := range rows {
		plainRows[r] = make([]string, len(row))
		for c, cell := range row {
			plainRows[r][c] = plainCell(cell)
			widths[c] = max(widths[c], displayWidth(plainRows[r][c]))
		}
	}
	total := 3 * (len(widths) - 1)
	for _, w := range widths {
		total += w
	}
	if total > tableMaxWidth && len(rows) > 0 {
		return renderTableCards(plainHeader, plainRows)
	}

	line := func(cells []string) string {
		parts := make([]string, len(cells))
		for c, cell := range cells {
			parts[c] = padCell(cell, widths[c], aligns[c])
		}
		return strings.TrimRight(strings.Join(parts, " | "), " ")
	}
	rule := make([]string, len(widths))
	for c, w := range widths {
		rule[c] = strings.Repeat("-", w)
	}
	out := []string{line(plainHeader), strings.Join(rule, "-+-")}
	for _, row := range plainRows {
		out = append(out, line(row))
	}
	return "<pre>" + html.EscapeString(strings.Join(out, "\n")) + "</pre>"
}

// renderTableCards is the lossless wide-table fallback: per data row a
// block of "<b>Header</b>: value" lines, blocks separated by a blank line.
func renderTableCards(header []string, rows [][]string) string {
	cards := make([]string, len(rows))
	for r, row := range rows {
		var lines []string
		for c, cell := range row {
			if cell == "" {
				continue
			}
			if header[c] == "" {
				lines = append(lines, html.EscapeString(cell))
				continue
			}
			lines = append(lines, "<b>"+html.EscapeString(header[c])+"</b>: "+html.EscapeString(cell))
		}
		cards[r] = strings.Join(lines, "\n")
	}
	return strings.Join(cards, "\n\n")
}
