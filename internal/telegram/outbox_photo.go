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
// One DB attempt, at most two API calls: there is never a second request after
// an ambiguous or unproven result. The only two-call path is a 400 whose
// description is exactly one of photoRefusalTokens, followed by one document of
// the same bytes. ASSUMPTION: such a 400 means Telegram stored nothing. Nothing
// in this repository proves it; verify it in the live smoke before relying on it.

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // registers the decoder used by image.DecodeConfig
	_ "image/png"  // registers the decoder used by image.DecodeConfig
	"io"
	"log/slog"
	"slices"
	"strings"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// Telegram's sendPhoto limits (Bot API): at most 10 MB, width plus height at
// most 10000 pixels, and a width to height ratio of at most 20.
const (
	maxPhotoBytes   = 10 << 20
	maxPhotoSideSum = 10000
	maxPhotoRatio   = 20
)

// codePhotoRefusedCanceled is a proven pre-delivery result (Telegram refused the
// photo, nothing was stored by assumption) that ended before the document could
// be sent. It is released for retry like any proven-unsent failure but, unlike
// canceled_before_dispatch, is not refunded, so it consumes the retry budget.
const codePhotoRefusedCanceled = "photo_refused_canceled"

// photoRefusalTokens are the 400 descriptions (after "Bad Request: ") treated as
// a refusal of the picture itself. ASSUMED FROM MEMORY of Telegram's behavior,
// not from documentation or anything in this repository: verify each in the
// live smoke. A description not listed here is NOT a photo refusal: the part is
// refused as an ordinary bad request and no document is sent.
var photoRefusalTokens = []string{"PHOTO_INVALID_DIMENSIONS", "IMAGE_PROCESS_FAILED"}

// photoFits reports whether data can be sent with sendPhoto. Only the image
// header is decoded. CMYK JPEG and animated PNG (APNG) are not photos: they go
// as documents so the bytes are kept as they are.
func photoFits(data []byte) bool {
	if len(data) == 0 || len(data) > maxPhotoBytes {
		return false
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return false
	}
	if cfg.ColorModel == color.CMYKModel || (format == "png" && isAnimatedPNG(data)) {
		return false
	}
	w, h := cfg.Width, cfg.Height
	// Each side is bounded before any sum, so the sum cannot overflow on any GOARCH.
	if w <= 0 || h <= 0 || w > maxPhotoSideSum || h > maxPhotoSideSum || w+h > maxPhotoSideSum {
		return false
	}
	return max(w, h) <= maxPhotoRatio*min(w, h) // symmetric: EXIF rotation does not matter
}

// isAnimatedPNG reports whether an acTL chunk precedes the first IDAT.
func isAnimatedPNG(data []byte) bool {
	const sig = 8
	for off := sig; off+8 <= len(data); {
		n := int(binary.BigEndian.Uint32(data[off:]))
		switch string(data[off+4 : off+8]) {
		case "acTL":
			return true
		case "IDAT", "IEND":
			return false
		}
		if n < 0 || n > len(data)-off-12 {
			return false
		}
		off += 12 + n
	}
	return false
}

// photoRefused reports whether err is the library's 400 for a refused picture.
// go-telegram v1.27.0 builds it as fmt.Errorf("%w, %s", ErrorBadRequest,
// description) with Telegram's description "Bad Request: <TOKEN>". The token
// must equal an entry of photoRefusalTokens; a substring match anywhere in the
// text is not accepted, and a wrapped or differently shaped error is not a match.
func photoRefused(err error) bool {
	if err == nil || !errors.Is(err, bot.ErrorBadRequest) {
		return false
	}
	desc, ok := strings.CutPrefix(err.Error(), bot.ErrorBadRequest.Error()+", ")
	if !ok {
		return false
	}
	tok, ok := strings.CutPrefix(desc, "Bad Request: ")
	return ok && slices.Contains(photoRefusalTokens, tok)
}

// uploadBound is the largest snapshot the transport will buffer: the opener's own
// bound when it states one, else the store's default per-file limit.
func (t *botTransport) uploadBound() int64 {
	if m, ok := t.artifacts.(interface{ MaxFileBytes() int64 }); ok && m.MaxFileBytes() > 0 {
		return m.MaxFileBytes()
	}
	return store.DefaultMaxArtifactFileBytes
}

// attemptPhoto delivers one photo-presented upload from the verified bytes.
func (t *botTransport) attemptPhoto(ctx context.Context, req SendRequest, name string, data io.Reader) Outcome {
	bound := t.uploadBound()
	buf, err := io.ReadAll(io.LimitReader(data, bound+1))
	if err != nil || int64(len(buf)) > bound {
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
		if ctx.Err() != nil {
			// Proven refusal, but the attempt is over: do not start a second
			// call whose timeout would be recorded as an uncertain outcome.
			// NOT "canceled_before_dispatch": the dispatcher refunds that code,
			// and Telegram did answer, so the attempt must stay counted or the
			// photo would be re-sent without a budget.
			return Outcome{Kind: OutcomeNotSent, Code: codePhotoRefusedCanceled}
		}
		if t.log != nil {
			t.log.Info("photo refused by telegram; sending the same bytes as a document",
				slog.String("part", req.PartID), slog.Int64("attempt_id", req.AttemptID))
		}
		return sendDoc()
	}
	return classifySend(msg, err, 0)
}
