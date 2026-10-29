// Package telegramhtml validates the bounded HTML subset used by frozen deliveries.
package telegramhtml

import (
	"encoding/xml"
	"html"
	"io"
	"net/url"
	"strings"
)

type sanitizedTag struct{ name, close string }

// XML's strict balancing prevents malformed nesting from reaching Telegram.
// Only the documented boolean blockquote attribute needs normalization.
func Sanitize(input string) (string, bool) {
	input = strings.ReplaceAll(input, "<blockquote expandable>", `<blockquote expandable="true">`)
	d := xml.NewDecoder(strings.NewReader(input))
	var out strings.Builder
	var stack []sanitizedTag
	for {
		start := d.InputOffset()
		token, err := d.Token()
		if err == io.EOF {
			return out.String(), len(stack) == 0
		}
		if err != nil {
			return "", false
		}
		raw := input[start:d.InputOffset()]
		if !writeToken(&out, &stack, token, raw) {
			return "", false
		}
	}
}

func writeToken(out *strings.Builder, stack *[]sanitizedTag, token xml.Token, raw string) bool {
	switch t := token.(type) {
	case xml.CharData:
		out.WriteString(html.EscapeString(string(t)))
	case xml.StartElement:
		return openToken(out, stack, t, raw)
	case xml.EndElement:
		if len(*stack) == 0 {
			return false
		}
		out.WriteString((*stack)[len(*stack)-1].close)
		*stack = (*stack)[:len(*stack)-1]
	default:
		out.WriteString(html.EscapeString(raw))
	}
	return true
}

func openToken(out *strings.Builder, stack *[]sanitizedTag, t xml.StartElement, raw string) bool {
	if len(*stack) >= 32 || len(raw) > 1024 || t.Name.Space != "" {
		return false
	}
	open, close := telegramTag(t)
	if !validRichNesting(*stack, t.Name.Local, open != "") {
		return false
	}
	if open == "" && close != "" {
		out.WriteString(html.EscapeString(raw))
		close = html.EscapeString("</" + t.Name.Local + ">")
	} else {
		out.WriteString(open)
	}
	*stack = append(*stack, sanitizedTag{t.Name.Local, close})
	return true
}

func validRichNesting(stack []sanitizedTag, name string, enabled bool) bool {
	if !enabled {
		return true
	}
	for _, parent := range stack {
		if parent.name == "code" || parent.name == "pre" {
			return false
		}
		if name == "blockquote" && parent.name == "blockquote" {
			return false
		}
		if name == "a" && parent.name == "a" {
			return false
		}
		if (name == "code" || name == "pre") && parent.name != "blockquote" {
			return false
		}
	}
	return true
}

func telegramTag(t xml.StartElement) (string, string) {
	name := t.Name.Local
	switch name {
	case "strong":
		name = "b"
	case "em":
		name = "i"
	case "ins":
		name = "u"
	case "strike", "del":
		name = "s"
	}
	switch name {
	case "b", "i", "u", "s", "code", "pre", "tg-spoiler":
		return "<" + name + ">", "</" + name + ">"
	case "blockquote":
		if _, ok := attribute(t, "expandable"); ok {
			return "<blockquote expandable>", "</blockquote>"
		}
		return "<blockquote>", "</blockquote>"
	case "span":
		if v, ok := attribute(t, "class"); ok && v == "tg-spoiler" {
			return "<tg-spoiler>", "</tg-spoiler>"
		}
	case "a":
		if v, ok := attribute(t, "href"); ok && allowedRichURL(v) {
			return `<a href="` + html.EscapeString(v) + `">`, "</a>"
		}
		return "", ""
	}
	return "", "escape"
}

func allowedRichURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return u.Host != ""
	case "tg":
		return u.Host != ""
	}
	return false
}

// Valid accepts only canonical, allowlisted output; admission must sanitize first.
func Valid(text string) bool { clean, ok := Sanitize(text); return ok && clean == text }

func attribute(t xml.StartElement, name string) (string, bool) {
	for _, a := range t.Attr {
		if a.Name.Space == "" && a.Name.Local == name {
			return a.Value, true
		}
	}
	return "", false
}
