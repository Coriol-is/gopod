package telegram

import (
	"errors"

	"strings"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/telegramhtml"
)

const ipcTextBytes = 3500

// PrepareIPCText freezes only validated Telegram HTML. Invalid nesting falls
// back to the original text, preserving content rather than losing a message.
func PrepareIPCText(text, format string) ([]store.DeliveryPart, error) {
	rendered := text
	switch format {
	case "", "markdown":
		rendered = finalReplyHTML(text)
	case "html":
	case "plain":
		return plainIPCParts(text), nil
	default:
		return nil, errors.New("invalid_format")
	}
	rendered, ok := sanitizeTelegramHTML(rendered)
	if !ok || strings.TrimSpace(htmlToPlain(rendered)) == "" {
		return plainIPCParts(text), nil
	}
	chunks := chunkHTML(rendered, ipcTextBytes)
	parts := make([]store.DeliveryPart, 0, len(chunks))
	for _, chunk := range chunks {
		if len(chunk) > ipcTextBytes {
			return plainIPCParts(text), nil
		}
		if _, ok := sanitizeTelegramHTML(chunk); !ok {
			return plainIPCParts(text), nil
		}
		parts = append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(chunk), ParseMode: "HTML"})
	}
	return parts, nil
}

func plainIPCParts(text string) []store.DeliveryPart {
	var parts []store.DeliveryPart
	for len(text) > ipcTextBytes {
		cut := ipcTextBytes
		for !utf8.RuneStart(text[cut]) {
			cut--
		}
		parts = append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(text[:cut])})
		text = text[cut:]
	}
	return append(parts, store.DeliveryPart{Kind: store.PartText, Payload: []byte(text)})
}

func sanitizeTelegramHTML(text string) (string, bool) { return telegramhtml.Sanitize(text) }
