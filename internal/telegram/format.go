package telegram

import (
	"html"
	"regexp"
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
	return renderMarkdown(md, 0)
}

// collapseOverLines is the line count above which a fenced code block
// in a final reply is shown collapsed (finalReplyHTML).
const collapseOverLines = 12

// finalReplyHTML is markdownToTelegramHTML for the agent's final reply:
// a fenced code block longer than collapseOverLines lines is wrapped in
// an expandable blockquote, which Telegram shows collapsed by default.
// Command replies keep markdownToTelegramHTML.
//
// Verified against the Bot API (core.telegram.org/bots/api#formatting-options,
// 2026-10-25): HTML mode supports <blockquote expandable>; the prose rules
// say pre/code contain nothing and "blockquote and expandable_blockquote
// entities can't be nested", and are vague on pre inside a quote ("all
// other entities can't contain each other"). The Bot API server is built
// on TDLib, whose validator (td/telegram/MessageEntity.cpp,
// are_entities_valid) states it outright: "Pre and Code can't be part of
// other entities, except blockquote". So <blockquote expandable><pre>
// is a valid nesting. No length limit is documented for an expandable
// quote; the message limit (4096 characters) applies, and chunkHTML
// closes and reopens both tags across chunks. If Telegram ever rejects
// the markup, sendChunks' plain-text fallback still delivers the text.
func finalReplyHTML(md string) string {
	return renderMarkdown(md, collapseOverLines)
}

// renderMarkdown converts md to Telegram HTML. A fenced block with more
// than collapseOver lines is wrapped in <blockquote expandable>;
// collapseOver 0 never collapses.
func renderMarkdown(md string, collapseOver int) string {
	var result strings.Builder
	lines := strings.Split(md, "\n")
	last := len(lines) - 1

	for i := 0; i < len(lines); i++ {
		line := lines[i]
		if isFence(line) {
			// The language identifier after ``` is dropped. A fence that is
			// never closed runs to the end of the text.
			end := i + 1
			for end < len(lines) && !isFence(lines[end]) {
				end++
			}
			body := lines[i+1 : end]
			collapse := collapseOver > 0 && len(body) > collapseOver
			if collapse {
				result.WriteString("<blockquote expandable>")
			}
			result.WriteString("<pre>")
			for k, l := range body {
				result.WriteString(html.EscapeString(l))
				if i+1+k < last {
					result.WriteString("\n")
				}
			}
			result.WriteString("</pre>")
			if collapse {
				result.WriteString("</blockquote>")
			}
			if end < last {
				result.WriteString("\n")
			}
			i = end
			continue
		}

		// Process inline markdown on this line.
		result.WriteString(processInlineMarkdown(html.EscapeString(line)))
		if i < last {
			result.WriteString("\n")
		}
	}
	return result.String()
}

// isFence reports whether line opens or closes a fenced code block.
func isFence(line string) bool {
	return strings.HasPrefix(line, "```")
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

// htmlTagRe matches one tag of renderMarkdown's output, whose literal
// < and > are always escaped.
var htmlTagRe = regexp.MustCompile(`<[^>]*>`)

// htmlToPlain turns renderMarkdown output back into plain text: tags
// dropped, entities unescaped. For a plain-text retry of an HTML edit
// Telegram rejected.
func htmlToPlain(s string) string {
	return html.UnescapeString(htmlTagRe.ReplaceAllString(s, ""))
}
