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
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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
	for _, desc := range photoRefusalTokens {
		api := &fakeAPI{msg: &models.Message{ID: 9}, photoErr: fmt.Errorf("%w, Bad Request: %s", bot.ErrorBadRequest, desc) /* library shape, see the real-library tests */}
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

// libAPI is the real go-telegram client against a local server, so the errors a
// refusal produces are built by the library itself, not by the test.
type libAPI struct {
	*bot.Bot
	mu    sync.Mutex
	calls map[string]int
}

func (l *libAPI) count(m string) int { l.mu.Lock(); defer l.mu.Unlock(); return l.calls[m] }

// newLibAPI answers sendPhoto with photoReply and sendDocument with docReply
// (both complete JSON bodies with an HTTP status).
func newLibAPI(t *testing.T, photoStatus int, photoReply string, docStatus int, docReply string) *libAPI {
	t.Helper()
	l := &libAPI{calls: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
		l.mu.Lock()
		l.calls[method]++
		l.mu.Unlock()
		status, body := docStatus, docReply
		if method == "sendPhoto" {
			status, body = photoStatus, photoReply
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	b, err := bot.New("123:test", bot.WithServerURL(srv.URL), bot.WithSkipGetMe(), bot.WithErrorsHandler(func(error) {}))
	if err != nil {
		t.Fatal(err)
	}
	l.Bot = b
	return l
}

const okMessage = `{"ok":true,"result":{"message_id":31,"chat":{"id":42}}}`

func apiFail(code int, desc, params string) string {
	if params != "" {
		params = `,"parameters":` + params
	}
	return fmt.Sprintf(`{"ok":false,"error_code":%d,"description":%q%s}`, code, desc, params)
}

func TestPhotoFallbackTriggerIsExactOnTheLibrarysRealError(t *testing.T) {
	data := realPNG(t, 50, 50)
	cases := []struct {
		name, desc string
		wantDoc    bool
	}{
		{"listed dimensions token", "Bad Request: PHOTO_INVALID_DIMENSIONS", true},
		{"listed processing token", "Bad Request: IMAGE_PROCESS_FAILED", true},
		{"token only inside other text", "Bad Request: chat not found (PHOTO_INVALID_DIMENSIONS)", false},
		{"token with a suffix", "Bad Request: PHOTO_INVALID_DIMENSIONS_X", false},
		{"lower case token", "Bad Request: photo_invalid_dimensions", false},
		{"dropped token", "Bad Request: PHOTO_SAVE_FILE_INVALID", false},
		{"no Bad Request prefix", "PHOTO_INVALID_DIMENSIONS", false},
		{"thread gone", "Bad Request: message thread not found", false},
		{"unlisted photo-like token", "Bad Request: PHOTO_EXT_INVALID", false},
	}
	for _, c := range cases {
		api := newLibAPI(t, 400, apiFail(400, c.desc, ""), 200, okMessage)
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if c.wantDoc {
			if out.Kind != OutcomeAck || api.count("sendPhoto") != 1 || api.count("sendDocument") != 1 {
				t.Fatalf("%s: %+v photo=%d doc=%d", c.name, out, api.count("sendPhoto"), api.count("sendDocument"))
			}
			continue
		}
		if out.Kind != OutcomeRefused || api.count("sendPhoto") != 1 || api.count("sendDocument") != 0 {
			t.Fatalf("%s: %+v photo=%d doc=%d", c.name, out, api.count("sendPhoto"), api.count("sendDocument"))
		}
	}
}

func TestPhotoNeverReplaysAfterAnyOtherRealLibraryFailure(t *testing.T) {
	data := realPNG(t, 50, 50)
	cases := map[string]struct {
		status int
		body   string
		kind   OutcomeKind
		code   string
	}{
		"migrated":     {400, apiFail(400, "Bad Request: group chat was upgraded to a supergroup chat", `{"migrate_to_chat_id":-100123}`), OutcomeRefused, "chat_migrated"},
		"unauthorized": {401, apiFail(401, "Unauthorized", ""), OutcomeRefused, "denied_credentials"},
		"forbidden":    {403, apiFail(403, "Forbidden: bot was blocked by the user", ""), OutcomeRefused, "forbidden"},
		"not found":    {404, apiFail(404, "Not Found", ""), OutcomeRefused, "not_found"},
		"conflict":     {409, apiFail(409, "Conflict: terminated by other getUpdates request", ""), OutcomeRefused, "conflict"},
		"rate limited": {429, apiFail(429, "Too Many Requests: retry after 3", `{"retry_after":3}`), OutcomeRateLimited, "rate_limited"},
		"bad gateway":  {502, apiFail(502, "Bad Gateway", ""), OutcomeAmbiguous, "server_error"},
		"html garbage": {200, "<html>proxy</html>", OutcomeAmbiguous, "transport_error"},
	}
	for name, c := range cases {
		api := newLibAPI(t, c.status, c.body, 200, okMessage)
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != c.kind || out.Code != c.code {
			t.Fatalf("%s: %+v", name, out)
		}
		if api.count("sendPhoto") != 1 || api.count("sendDocument") != 0 {
			t.Fatalf("%s: photo=%d doc=%d; a second upload must not follow", name, api.count("sendPhoto"), api.count("sendDocument"))
		}
	}
}

// ok:true without a usable message id is accepted-but-untrusted: it must stay
// ambiguous and must never be followed by a document.
func TestPhotoSuccessWithoutAMessageIDIsMalformedAndNotReplayed(t *testing.T) {
	data := realPNG(t, 50, 50)
	for name, body := range map[string]string{
		"null result":  `{"ok":true,"result":null}`,
		"zero id":      `{"ok":true,"result":{"message_id":0,"chat":{"id":42}}}`,
		"empty result": `{"ok":true,"result":{}}`,
	} {
		api := newLibAPI(t, 200, body, 200, okMessage)
		out := photoTransport(api, data).Attempt(t.Context(), photoReq())
		if out.Kind != OutcomeAmbiguous || out.Code != "malformed_reply" || api.count("sendDocument") != 0 {
			t.Fatalf("%s: %+v doc=%d", name, out, api.count("sendDocument"))
		}
	}
	fake := &fakeAPI{} // nil message, nil error
	if out := photoTransport(fake, data).Attempt(t.Context(), photoReq()); out.Code != "malformed_reply" || fake.docs != 0 {
		t.Fatalf("nil message: %+v docs=%d", out, fake.docs)
	}
}

func TestPhotoRefusalWithAnEndedContextSendsNoDocument(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	api := &cancelOnPhoto{fakeAPI: fakeAPI{msg: &models.Message{ID: 1}, photoErr: fmt.Errorf("%w, Bad Request: PHOTO_INVALID_DIMENSIONS", bot.ErrorBadRequest)}, cancel: cancel}
	out := photoTransport(api, realPNG(t, 50, 50)).Attempt(ctx, photoReq())
	if out.Kind != OutcomeNotSent || out.Code != "canceled_before_dispatch" || api.docs != 0 {
		t.Fatalf("%+v docs=%d", out, api.docs)
	}
}

type cancelOnPhoto struct {
	fakeAPI
	cancel context.CancelFunc
}

func (c *cancelOnPhoto) SendPhoto(ctx context.Context, p *bot.SendPhotoParams) (*models.Message, error) {
	m, err := c.fakeAPI.SendPhoto(ctx, p)
	c.cancel()
	return m, err
}

func TestPhotoSideOverflowAndHugeDimensionsGoOutAsDocuments(t *testing.T) {
	for name, data := range map[string][]byte{
		"int32 max both": headerPNG(0x7fffffff, 0x7fffffff),
		"65535 both":     headerPNG(65535, 65535),
		"one side 10001": headerPNG(10001, 1),
	} {
		api := &fakeAPI{msg: &models.Message{ID: 5}}
		photoTransport(api, data).Attempt(t.Context(), photoReq())
		if api.photos != 0 || api.docs != 1 {
			t.Fatalf("%s: photos=%d docs=%d", name, api.photos, api.docs)
		}
	}
}

// jpegHeader is a bare JPEG header (SOI + SOF) that image.DecodeConfig accepts.
func jpegHeader(sof byte, w, h uint16, comps int) []byte {
	jfif := []byte{0xFF, 0xE0, 0, 16, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0} // DecodeConfig stops at SOF only for JFIF
	b := append([]byte{0xFF, 0xD8}, jfif...)
	b = append(b, 0xFF, sof, 0, byte(8+3*comps), 8, byte(h>>8), byte(h), byte(w>>8), byte(w), byte(comps))
	for i := 0; i < comps; i++ {
		b = append(b, byte(i+1), 0x11, 0)
	}
	return b
}

func apngWithActl(t *testing.T) []byte {
	t.Helper()
	plain := realPNG(t, 20, 20)
	ihdrEnd := 8 + 8 + 13 + 4
	chunk := func(kind string, body []byte) []byte {
		var c bytes.Buffer
		_ = binary.Write(&c, binary.BigEndian, uint32(len(body)))
		c.WriteString(kind)
		c.Write(body)
		_ = binary.Write(&c, binary.BigEndian, crc32.ChecksumIEEE(append([]byte(kind), body...)))
		return c.Bytes()
	}
	out := append([]byte{}, plain[:ihdrEnd]...)
	out = append(out, chunk("acTL", []byte{0, 0, 0, 2, 0, 0, 0, 0})...)
	return append(out, plain[ihdrEnd:]...)
}

func TestPhotoFitsRoutesUnsupportedEncodingsToDocuments(t *testing.T) {
	var prog bytes.Buffer
	_ = jpeg.Encode(&prog, image.NewGray(image.Rect(0, 0, 64, 64)), nil)
	progressive := append([]byte{}, prog.Bytes()...)
	for i := 0; i+1 < len(progressive); i++ { // baseline SOF0 -> progressive SOF2
		if progressive[i] == 0xFF && progressive[i+1] == 0xC0 {
			progressive[i+1] = 0xC2
			break
		}
	}
	var rgb16 bytes.Buffer
	_ = png.Encode(&rgb16, image.NewRGBA64(image.Rect(0, 0, 30, 30)))
	cases := []struct {
		name string
		data []byte
		want bool
	}{
		{"cmyk jpeg", jpegHeader(0xC0, 100, 100, 4), false},
		{"rgb jpeg header", jpegHeader(0xC0, 100, 100, 3), true},
		{"apng", apngWithActl(t), false},
		{"plain png control", realPNG(t, 20, 20), true},
		{"progressive jpeg", progressive, true},
		{"16-bit png", rgb16.Bytes(), true},
		{"tall exif-rotatable 1x20", headerPNG(1, 20), true},
		{"wide exif-rotatable 20x1", headerPNG(20, 1), true},
		{"tall 1x21", headerPNG(1, 21), false},
		{"wide 21x1", headerPNG(21, 1), false},
	}
	// The CMYK case must be rejected for its colour model, not for a broken header.
	if cfg, _, err := image.DecodeConfig(bytes.NewReader(jpegHeader(0xC0, 100, 100, 4))); err != nil || cfg.ColorModel != color.CMYKModel {
		t.Fatalf("cmyk header not decoded as CMYK: %v %v", cfg.ColorModel, err)
	}
	for _, c := range cases {
		if got := photoFits(c.data); got != c.want {
			t.Errorf("%s: photoFits = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestPhotoCaptionIsPassedThroughUntouched(t *testing.T) {
	caption := strings.Repeat("\U0001F600", 1024) // 1024 runes, 2048 UTF-16 units
	api := &fakeAPI{msg: &models.Message{ID: 2}}
	req := photoReq()
	req.Text = caption
	photoTransport(api, realPNG(t, 20, 20)).Attempt(t.Context(), req)
	if api.lastPhoto == nil || api.lastPhoto.Caption != caption {
		t.Fatal("caption changed or photo not sent")
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("disk read failed") }

type readerOpener struct{ r io.Reader }

func (o readerOpener) OpenArtifact(context.Context, string, string, int64) (string, io.Reader, error) {
	return "x.png", o.r, nil
}

type boundedOpener struct {
	readerOpener
	max int64
}

func (o boundedOpener) MaxFileBytes() int64 { return o.max }

func TestPhotoSnapshotReadFailureAndOversizeAreRefusedBeforeAnyCall(t *testing.T) {
	api := &fakeAPI{msg: &models.Message{ID: 1}}
	if out := (&botTransport{api: api, artifacts: readerOpener{failingReader{}}}).Attempt(t.Context(), photoReq()); out.Kind != OutcomeRefused || out.Code != "artifact_unavailable" {
		t.Fatalf("read error: %+v", out)
	}
	data := realPNG(t, 20, 20)
	tr := &botTransport{api: api, artifacts: boundedOpener{readerOpener{bytes.NewReader(data)}, int64(len(data)) - 1}}
	if out := tr.Attempt(t.Context(), photoReq()); out.Kind != OutcomeRefused || out.Code != "artifact_unavailable" {
		t.Fatalf("over the bound: %+v", out)
	}
	tr = &botTransport{api: api, artifacts: boundedOpener{readerOpener{bytes.NewReader(data)}, int64(len(data))}}
	if out := tr.Attempt(t.Context(), photoReq()); out.Kind != OutcomeAck {
		t.Fatalf("exactly at the bound: %+v", out)
	}
	if api.photos != 1 || api.docs != 0 {
		t.Fatalf("photos=%d docs=%d", api.photos, api.docs)
	}
}
