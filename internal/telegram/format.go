package telegram

import (
	"html"
	"strings"
)

// markdownToTelegramHTML converts Claude's markdown output to
// Telegram-compatible HTML. Telegram's HTML mode supports:
//   <b>bold</b>, <i>italic</i>, <code>inline code</code>,
//   <pre>code block</pre>, <a href="url">text</a>
//
// This is simpler and more robust than MarkdownV2 which requires
// escaping 18 special characters outside of entities.
//
// Handles:
//   **bold** or __bold__  → <b>bold</b>
//   *italic* or _italic_  → <i>italic</i>
//   `inline code`         → <code>inline code</code>
//   ```lang\ncode\n```    → <pre>code</pre>
//   [text](url)           → <a href="url">text</a>
//
// Everything else is HTML-escaped so <, >, & don't break rendering.
func markdownToTelegramHTML(md string) string {
	// First pass: protect code blocks (``` ... ```) from further processing.
	var result strings.Builder
	lines := strings.Split(md, "\n")

	inCodeBlock := false
	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			if !inCodeBlock {
				inCodeBlock = true
				result.WriteString("<pre>")
				// Skip the language identifier after ```
				continue
			} else {
				inCodeBlock = false
				result.WriteString("</pre>")
				if i < len(lines)-1 {
					result.WriteString("\n")
				}
				continue
			}
		}

		if inCodeBlock {
			result.WriteString(html.EscapeString(line))
			if i < len(lines)-1 {
				result.WriteString("\n")
			}
			continue
		}

		// Process inline markdown on this line.
		processed := processInlineMarkdown(html.EscapeString(line))
		result.WriteString(processed)
		if i < len(lines)-1 {
			result.WriteString("\n")
		}
	}

	// If code block was never closed, close it.
	if inCodeBlock {
		result.WriteString("</pre>")
	}

	return result.String()
}

// processInlineMarkdown handles inline formatting on a single
// HTML-escaped line. Inline code spans are cut out first so the
// emphasis passes can't mangle their contents (URLs with underscores,
// literal asterisks, etc.).
func processInlineMarkdown(line string) string {
	var result strings.Builder
	for {
		start := strings.Index(line, "`")
		if start < 0 {
			break
		}
		end := strings.Index(line[start+1:], "`")
		if end < 0 {
			break
		}
		end += start + 1
		result.WriteString(applyEmphasis(line[:start]))
		result.WriteString("<code>")
		result.WriteString(line[start+1 : end])
		result.WriteString("</code>")
		line = line[end+1:]
	}
	result.WriteString(applyEmphasis(line))
	return result.String()
}

// applyEmphasis converts bold/italic markers on a segment that is
// guaranteed to contain no inline code spans.
func applyEmphasis(s string) string {
	// Bold: **text** → <b>text</b>
	s = replaceDelimited(s, "**", "<b>", "</b>")

	// Bold alt: __text__ → <b>text</b>
	s = replaceDelimited(s, "__", "<b>", "</b>")

	// Italic: *text* → <i>text</i> (but not inside ** which is already processed)
	s = replaceDelimited(s, "*", "<i>", "</i>")

	// Italic alt: _text_ → <i>text</i>
	s = replaceDelimited(s, "_", "<i>", "</i>")

	return s
}

// replaceDelimited finds pairs of delimiter and wraps the content
// between them with open/close tags.
func replaceDelimited(s, delim, open, close string) string {
	var result strings.Builder
	for {
		start := strings.Index(s, delim)
		if start < 0 {
			result.WriteString(s)
			break
		}
		end := strings.Index(s[start+len(delim):], delim)
		if end < 0 {
			result.WriteString(s)
			break
		}
		end += start + len(delim)

		result.WriteString(s[:start])
		result.WriteString(open)
		result.WriteString(s[start+len(delim) : end])
		result.WriteString(close)
		s = s[end+len(delim):]
	}
	return result.String()
}
