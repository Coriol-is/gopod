package telegram

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"testing"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// bytesOpener returns fixed bytes as the verified snapshot.
type bytesOpener struct{ data []byte }

func (o bytesOpener) OpenArtifact(context.Context, string, string, int64) (string, io.Reader, error) {
	return "chart.png", bytes.NewReader(o.data), nil
}

func realPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, image.NewGray(image.Rect(0, 0, w, h))); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

// headerPNG is a PNG whose header declares w x h without the pixel data. It is
// enough for image.DecodeConfig, which is all the size rules read.
func headerPNG(w, h uint32) []byte {
	chunk := func(kind string, body []byte) []byte {
		var c bytes.Buffer
		_ = binary.Write(&c, binary.BigEndian, uint32(len(body)))
		c.WriteString(kind)
		c.Write(body)
		_ = binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), body...)))
		return c.Bytes()
	}
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 0 // 8-bit grayscale
	out := []byte("\x89PNG\r\n\x1a\n")
	out = append(out, chunk("IHDR", ihdr)...)
	return append(out, chunk("IDAT", nil)...)
}

func photoReq() SendRequest {
	return SendRequest{Kind: store.PartUpload, ChatID: 42, Thread: 3, ArtifactRef: "a", ParseMode: store.UploadPresentationPhoto, Text: "caption"}
}

func photoTransport(api outboxAPI, data []byte) *botTransport {
	return &botTransport{api: api, artifacts: bytesOpener{data}}
}

func TestPhotoPresentationSendsSendPhotoFromTheSnapshotBytes(t *testing.T) {
	for name, data := range map[string][]byte{"png": realPNG(t, 120, 60), "jpeg": func() []byte {
		var b bytes.Buffer
		_ = jpeg.Encode(&b, image.NewGray(image.Rect(0, 0, 64, 64)), nil)
		return b.Bytes()
	}()} {
		api := &fakeAPI{msg: &models.Message{ID: 77}}
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != OutcomeAck || out.RemoteMessageID != 77 {
			t.Fatalf("%s: %+v", name, out)
		}
		if api.photos != 1 || api.docs != 0 || !bytes.Equal(api.photoBytes, data) {
			t.Fatalf("%s: photos=%d docs=%d same bytes=%v", name, api.photos, api.docs, bytes.Equal(api.photoBytes, data))
		}
		p := api.lastPhoto
		if p.ChatID != int64(42) || p.MessageThreadID != 3 || p.Caption != "caption" || p.ParseMode != "" {
			t.Fatalf("%s: destination/caption: %+v", name, p)
		}
	}
}

func TestPhotoThatBreaksALimitGoesOutAsADocumentWithoutAPhotoCall(t *testing.T) {
	big := append(realPNG(t, 10, 10), make([]byte, maxPhotoBytes)...) // valid header, over 10 MB
	gifBytes := func() []byte {
		var b bytes.Buffer
		_ = gif.Encode(&b, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil)
		return b.Bytes()
	}()
	cases := map[string][]byte{
		"over 10 MB":          big,
		"side sum over 10000": headerPNG(6000, 4001),
		"ratio over 20":       realPNG(t, 1, 21),
		"zero size":           headerPNG(0, 10),
		"not an image":        []byte("plain text, not a picture"),
		"gif":                 gifBytes,
		"truncated header":    realPNG(t, 10, 10)[:20],
	}
	for name, data := range cases {
		api := &fakeAPI{msg: &models.Message{ID: 5}}
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != OutcomeAck {
			t.Fatalf("%s: %+v", name, out)
		}
		if api.photos != 0 || api.docs != 1 || !bytes.Equal(api.docBytes, data) {
			t.Fatalf("%s: photos=%d docs=%d same bytes=%v", name, api.photos, api.docs, bytes.Equal(api.docBytes, data))
		}
		if api.lastDoc.Caption != "caption" || api.lastDoc.MessageThreadID != 3 {
			t.Fatalf("%s: document destination: %+v", name, api.lastDoc)
		}
	}
}

func TestPhotoLimitsAcceptTheBoundaries(t *testing.T) {
	for name, data := range map[string][]byte{
		"side sum exactly 10000": headerPNG(5000, 5000),
		"ratio exactly 20":       headerPNG(20, 1),
	} {
		api := &fakeAPI{msg: &models.Message{ID: 5}}
		photoTransport(api, data).Attempt(t.Context(), photoReq())
		if api.photos != 1 || api.docs != 0 {
			t.Fatalf("%s: photos=%d docs=%d", name, api.photos, api.docs)
		}
	}
}

func TestPhotoExplicitlyRejectedByTelegramFallsBackToTheSameBytesAsADocument(t *testing.T) {
	data := realPNG(t, 50, 50)
	for _, desc := range []string{"PHOTO_INVALID_DIMENSIONS", "IMAGE_PROCESS_FAILED", "PHOTO_SAVE_FILE_INVALID"} {
		api := &fakeAPI{msg: &models.Message{ID: 9}, photoErr: fmt.Errorf("%w, Bad Request: %s", bot.ErrorBadRequest, desc)}
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != OutcomeAck || out.RemoteMessageID != 9 {
			t.Fatalf("%s: %+v", desc, out)
		}
		if api.photos != 1 || api.docs != 1 || !bytes.Equal(api.docBytes, data) {
			t.Fatalf("%s: photos=%d docs=%d same bytes=%v", desc, api.photos, api.docs, bytes.Equal(api.docBytes, data))
		}
	}
}

// Only a proven refusal of the photo itself may produce a second upload.
func TestPhotoFailureOtherThanAPhotoRefusalNeverSendsADocument(t *testing.T) {
	data := realPNG(t, 50, 50)
	cases := map[string]struct {
		err  error
		kind OutcomeKind
	}{
		"timeout":         {context.DeadlineExceeded, OutcomeAmbiguous},
		"5xx":             {errors.New("error response from telegram for method sendPhoto, 502 Bad Gateway"), OutcomeAmbiguous},
		"transport error": {errors.New("connection reset"), OutcomeAmbiguous},
		"rate limited":    {&bot.TooManyRequestsError{RetryAfter: 4}, OutcomeRateLimited},
		"forbidden":       {fmt.Errorf("%w, blocked", bot.ErrorForbidden), OutcomeRefused},
		"thread gone":     {fmt.Errorf("%w, message thread not found", bot.ErrorBadRequest), OutcomeRefused},
		"other 400":       {fmt.Errorf("%w, chat not found", bot.ErrorBadRequest), OutcomeRefused},
	}
	for name, c := range cases {
		api := &fakeAPI{msg: &models.Message{ID: 9}, photoErr: c.err}
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != c.kind {
			t.Fatalf("%s: %+v", name, out)
		}
		if api.photos != 1 || api.docs != 0 {
			t.Fatalf("%s: photos=%d docs=%d; a second upload must not follow", name, api.photos, api.docs)
		}
	}
}

func TestPhotoFallbackDocumentFailureIsClassifiedAndNotRepeated(t *testing.T) {
	api := &fakeAPI{err: context.DeadlineExceeded, photoErr: fmt.Errorf("%w, Bad Request: PHOTO_INVALID_DIMENSIONS", bot.ErrorBadRequest)}
	out := photoTransport(api, realPNG(t, 50, 50)).Attempt(t.Context(), photoReq())
	if out.Kind != OutcomeAmbiguous || api.photos != 1 || api.docs != 1 {
		t.Fatalf("%+v photos=%d docs=%d", out, api.photos, api.docs)
	}
}

func TestPhotoWithUnreadableArtifactIsRefusedBeforeAnyCall(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 1}}
	tr := &botTransport{api: api, artifacts: fakeOpener{err: errors.New("gone")}}
	if out := tr.Attempt(t.Context(), photoReq()); out.Kind != OutcomeRefused || out.Code != "artifact_unavailable" || api.photos+api.docs != 0 {
		t.Fatalf("%+v", out)
	}
	if out := (&botTransport{api: api}).Attempt(t.Context(), photoReq()); out.Code != "upload_unavailable" {
		t.Fatalf("no opener: %+v", out)
	}
}

func TestUnknownUploadPresentationIsStillRefused(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 1}}
	req := photoReq()
	req.ParseMode = "HTML"
	if out := photoTransport(api, realPNG(t, 4, 4)).Attempt(t.Context(), req); out.Code != "invalid_presentation" || api.photos+api.docs != 0 {
		t.Fatalf("%+v", out)
	}
}

func TestDocumentPresentationNeverUsesSendPhoto(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 1}}
	req := photoReq()
	req.ParseMode = "" // document is the default and the original-quality choice
	photoTransport(api, realPNG(t, 40, 40)).Attempt(t.Context(), req)
	if api.photos != 0 || api.docs != 1 {
		t.Fatalf("photos=%d docs=%d", api.photos, api.docs)
	}
}

func TestPhotoUploadUsesTheVerifiedSnapshotAndNeverReopensThePath(t *testing.T) {
	data := realPNG(t, 80, 40)
	f := newOpenerFixture(t, string(data))
	api := &fakeAPI{msg: &models.Message{ID: 6}}
	req := f.uploadReq()
	req.ParseMode = store.UploadPresentationPhoto
	out := (&botTransport{api: api, artifacts: f.opener()}).Attempt(t.Context(), req)
	if out.Kind != OutcomeAck || api.photos != 1 || api.docs != 0 || !bytes.Equal(api.photoBytes, data) {
		t.Fatalf("%+v photos=%d docs=%d same=%v", out, api.photos, api.docs, bytes.Equal(api.photoBytes, data))
	}
	if up := api.lastPhoto.Photo.(*models.InputFileUpload); up.Filename != "report.csv" {
		t.Fatalf("filename %q", up.Filename)
	}
}
