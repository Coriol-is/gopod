package telegram

import (
	"strings"
	"unicode/utf8"
)

// chunkHTML splits Telegram HTML into pieces of at most n bytes, each a
// well-formed message on its own. It prefers the same boundaries as
// chunkString (paragraph, line, space), never cuts inside a tag or an
// HTML entity or a UTF-8 sequence, and when a boundary falls inside open
// elements (<pre>, <blockquote expandable>, <code>, <b>, <a href=...>)
// it closes them at the end of the chunk and reopens them, with the same
// attributes, at the start of the next one. Without this a code block
// split across two messages arrives as unbalanced markup, Telegram
// rejects it, and the chunk falls back to plain text.
//
// The input is what renderMarkdown produces: every literal <, > and &
// is escaped, so a '<' always starts a tag and a '&' an entity.
func chunkHTML(s string, n int) []string {
	if len(s) <= n {
		return []string{s}
	}
	var out []string
	var carry []htmlTag // elements open at the start of s
	for {
		// Elements that close right where the previous chunk ended were
		// already closed there; reopening them would leave empty ones.
		for len(carry) > 0 && strings.HasPrefix(s, "</"+carry[len(carry)-1].name+">") {
			s = s[len("</"+carry[len(carry)-1].name+">"):]
			carry = carry[:len(carry)-1]
		}
		prefix := openTags(carry)
		if len(prefix)+len(s) <= n {
			if s != "" {
				out = append(out, prefix+s)
			}
			return out
		}
		budget := max(n-len(prefix), 1)
		var split int
		var stack []htmlTag
		for {
			split = htmlSplit(s, budget)
			stack = tagStack(carry, s[:split])
			need := len(prefix) + split + closersLen(stack)
			if need <= n || budget == 1 {
				break
			}
			// The closers did not fit: retry with room for them.
			budget = max(min(budget-1, n-len(prefix)-closersLen(stack)), 1)
		}
		out = append(out, prefix+s[:split]+closeTags(stack))
		s = s[split:]
		carry = stack
		if s == "" {
			return out
		}
	}
}

// htmlTag is an element open at a chunk boundary: its name and the
// opening tag as written, attributes included.
type htmlTag struct {
	name string
	open string
}

// htmlSplit picks where to cut s, at most limit bytes in (limit <
// len(s)). Boundaries are tried in order "\n\n", "\n", " ", each only
// in the last two thirds of the window and outside markup; otherwise a
// hard cut backed off out of markup and to a rune start. Never 0.
func htmlSplit(s string, limit int) int {
	for _, sep := range []string{"\n\n", "\n", " "} {
		i := strings.LastIndex(s[:limit], sep)
		for i > limit/3 {
			cut := i + len(sep)
			if cut <= limit && !insideMarkup(s, cut) {
				return trimOpenTags(s, cut)
			}
			i = strings.LastIndex(s[:i], sep)
		}
	}
	cut := limit
	for cut > 0 && (insideMarkup(s, cut) || !utf8.RuneStart(s[cut])) {
		cut--
	}
	if cut == 0 {
		// A single tag or entity longer than the window: cut after it.
		if end := strings.IndexAny(s, ">;"); end >= 0 {
			return end + 1
		}
		return limit
	}
	return trimOpenTags(s, cut)
}

// trimOpenTags moves a cut that falls right after opening tags to before
// them, so a chunk does not end with an element that is empty. Returns
// cut unchanged when that would leave nothing.
func trimOpenTags(s string, cut int) int {
	c := cut
	for c > 0 && s[c-1] == '>' {
		lt := strings.LastIndexByte(s[:c], '<')
		if lt < 0 || s[lt+1] == '/' {
			break
		}
		c = lt
	}
	if c == 0 {
		return cut
	}
	return c
}

// insideMarkup reports whether cutting s at cut splits a tag or an
// entity.
func insideMarkup(s string, cut int) bool {
	head := s[:cut]
	if strings.LastIndexByte(head, '<') > strings.LastIndexByte(head, '>') {
		return true
	}
	amp := strings.LastIndexByte(head, '&')
	return amp >= 0 && cut-amp <= 10 && !strings.ContainsAny(head[amp:], "; \n")
}

// tagStack returns the elements open after seg, given those open before.
func tagStack(open []htmlTag, seg string) []htmlTag {
	stack := append([]htmlTag(nil), open...)
	for {
		lt := strings.IndexByte(seg, '<')
		if lt < 0 {
			return stack
		}
		gt := strings.IndexByte(seg[lt:], '>')
		if gt < 0 {
			return stack
		}
		tag := seg[lt : lt+gt+1]
		seg = seg[lt+gt+1:]
		if strings.HasPrefix(tag, "</") {
			name := tag[2 : len(tag)-1]
			for k := len(stack) - 1; k >= 0; k-- {
				if stack[k].name == name {
					stack = stack[:k]
					break
				}
			}
			continue
		}
		name := strings.TrimSuffix(tag[1:], ">")
		if i := strings.IndexAny(name, " \t\n"); i >= 0 {
			name = name[:i]
		}
		stack = append(stack, htmlTag{name: name, open: tag})
	}
}

// openTags renders the opening tags of stack, outermost first.
func openTags(stack []htmlTag) string {
	var sb strings.Builder
	for _, t := range stack {
		sb.WriteString(t.open)
	}
	return sb.String()
}

// closeTags renders the closing tags of stack, innermost first.
func closeTags(stack []htmlTag) string {
	var sb strings.Builder
	for k := len(stack) - 1; k >= 0; k-- {
		sb.WriteString("</" + stack[k].name + ">")
	}
	return sb.String()
}

// closersLen is len(closeTags(stack)).
func closersLen(stack []htmlTag) int {
	n := 0
	for _, t := range stack {
		n += len(t.name) + 3
	}
	return n
}
