package telegram

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/store"
)

// namedOpener returns fixed bytes under the snapshot's recorded filename.
type namedOpener struct {
	name string
	data []byte
}

func (o namedOpener) OpenArtifact(context.Context, string, string, int64) (string, io.Reader, error) {
	return o.name, bytes.NewReader(o.data), nil
}

// CP-040: an agent attachment presented as a document (as "document", or
// "auto" on a non-image) goes out through sendDocument with the original
// filename, the plain caption and the destination thread; a photo marker on
// bytes that cannot be a photo (a PDF behind a .png name) still lands as a
// document under the same filename.
func TestAttachmentDocumentKeepsOriginalFilenameAndCaption(t *testing.T) {
	pdf := []byte("%PDF-1.4\n%binary\n")
	cases := []struct {
		name, file, marker string
	}{
		{"document part", "preduzetnik-bank-assessment.pdf", ""},
		{"photo marker on non-image bytes", "chart.png", store.UploadPresentationPhoto},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			api := &fakeAPI{msg: &models.Message{ID: 9}}
			tr := &botTransport{api: api, artifacts: namedOpener{c.file, pdf}}
			req := SendRequest{Kind: store.PartUpload, ChatID: 42, Thread: 5, ArtifactRef: "a", ParseMode: c.marker, Text: "Оценка банка"}
			out := tr.Attempt(t.Context(), req)
			if out.Kind != OutcomeAck {
				t.Fatalf("outcome %+v", out)
			}
			if api.docs != 1 || api.photos != 0 {
				t.Fatalf("docs=%d photos=%d", api.docs, api.photos)
			}
			p := api.lastDoc
			up, ok := p.Document.(*models.InputFileUpload)
			if !ok || up.Filename != c.file || p.Caption != "Оценка банка" || p.ParseMode != "" || p.MessageThreadID != 5 || p.ChatID != int64(42) {
				t.Fatalf("document params %+v (upload %+v)", p, up)
			}
			if !bytes.Equal(api.docBytes, pdf) {
				t.Fatal("document bytes differ from the snapshot")
			}
		})
	}
}
