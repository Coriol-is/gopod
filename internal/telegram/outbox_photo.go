package telegram

// Photo presentation for uploads (CP-003 image delivery).
//
// A photo is only a presentation hint over the same immutable snapshot bytes a
// document would send. Telegram recompresses photos, caps them at 10 MB and
// restricts their shape, so the transport sends sendPhoto only when the bytes
// are a PNG or JPEG inside those limits and sends a document otherwise. The
// document is the original-quality form and is also what an upload with no
// presentation always gets.
//
// The one-attempt rule of the transport holds for the network outcome: there is
// never a second request after an ambiguous or unproven result. The only
// two-request path is an explicit "Bad Request" naming the photo itself, which
// proves Telegram stored nothing, followed by one document of the same bytes.

import (
	"bytes"
	"context"
	"errors"
	"image"
	_ "image/jpeg" // registers the decoder used by image.DecodeConfig
	_ "image/png"  // registers the decoder used by image.DecodeConfig
	"io"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// Telegram's sendPhoto limits (Bot API): at most 10 MB, width plus height at
// most 10000 pixels, and a width to height ratio of at most 20.
const (
	maxPhotoBytes    = 10 << 20
	maxPhotoSideSum  = 10000
	maxPhotoRatio    = 20
	maxSnapshotBytes = 64 << 20 // defensive read bound; the opener's own bound is the store's file limit
)

// photoFits reports whether data can be sent with sendPhoto. Only the image
// header is decoded.
func photoFits(data []byte) bool {
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return false
	}
	w, h := cfg.Width, cfg.Height
	if w <= 0 || h <= 0 || w+h > maxPhotoSideSum {
		return false
	}
	return max(w, h) <= maxPhotoRatio*min(w, h)
}

// photoRefused reports whether err is Telegram refusing the photo itself (a
// 400 naming the picture), as opposed to the destination, the caption or an
// unknown failure. Only this proves a document of the same bytes is safe to send.
func photoRefused(err error) bool {
	if err == nil || !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	msg := strings.ToUpper(err.Error())
	return strings.Contains(msg, "PHOTO_INVALID_DIMENSIONS") ||
		strings.Contains(msg, "PHOTO_SAVE_FILE_INVALID") ||
		strings.Contains(msg, "PHOTO_EXT_INVALID") ||
		strings.Contains(msg, "IMAGE_PROCESS_FAILED")
}

// attemptPhoto delivers one photo-presented upload from the verified bytes.
func (t *botTransport) attemptPhoto(ctx context.Context, req SendRequest, name string, data io.Reader) Outcome {
	buf, err := io.ReadAll(io.LimitReader(data, maxSnapshotBytes+1))
	if err != nil || len(buf) > maxSnapshotBytes {
		return refused("artifact_unavailable")
	}
	sendDoc := func() Outcome {
		msg, err := t.api.SendDocument(ctx, &bot.SendDocumentParams{
			ChatID: req.ChatID, MessageThreadID: req.Thread,
			Document: &models.InputFileUpload{Filename: name, Data: bytes.NewReader(buf)},
			Caption:  req.Text,
		})
		return classifySend(msg, err, 0)
	}
	if !photoFits(buf) {
		return sendDoc()
	}
	msg, err := t.api.SendPhoto(ctx, &bot.SendPhotoParams{
		ChatID: req.ChatID, MessageThreadID: req.Thread,
		Photo:   &models.InputFileUpload{Filename: name, Data: bytes.NewReader(buf)},
		Caption: req.Text, // plain text; an upload has no parse mode
	})
	if photoRefused(err) {
		return sendDoc()
	}
	return classifySend(msg, err, 0)
}
