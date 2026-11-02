package ipc

import (
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// The request presentation is a hint that reaches the part: the legacy
// presentation field keeps D052 semantics, the CP-040 "as" field defaults to
// auto (PNG/JPEG name or MIME offered as a photo, everything else a document),
// and nothing an agent writes can select the host-only voice marker.
func TestArtifactPartsMapRequestPresentation(t *testing.T) {
	id := []string{"11111111-1111-4111-8111-111111111111"}
	cases := []struct {
		name string
		file artifactFile
		want string
	}{
		{"photo", artifactFile{Path: "/workspace/chat/a.png", Mime: "image/png", Presentation: "photo"}, store.UploadPresentationPhoto},
		{"photo on a non-image MIME", artifactFile{Path: "/workspace/chat/a.txt", Mime: "text/plain", Presentation: "photo"}, store.UploadPresentationPhoto},
		{"document", artifactFile{Path: "/workspace/chat/a.png", Mime: "image/png", Presentation: "document"}, ""},
		{"omitted as is auto: a PNG is offered as a photo", artifactFile{Path: "/workspace/chat/a.png"}, store.UploadPresentationPhoto},
		{"auto on a JPEG MIME is a photo", artifactFile{Path: "/workspace/chat/a.bin", Mime: "image/jpeg", As: "auto"}, store.UploadPresentationPhoto},
		{"auto on a PDF is a document", artifactFile{Path: "/workspace/chat/report.pdf", As: "auto"}, ""},
		{"auto on a GIF is a document", artifactFile{Path: "/workspace/chat/a.gif"}, ""},
		{"as document forces a document for an image", artifactFile{Path: "/workspace/chat/a.png", As: "document"}, ""},
		{"as photo", artifactFile{Path: "/workspace/chat/a.JPG", As: "photo"}, store.UploadPresentationPhoto},
		{"legacy presentation document keeps an image a document", artifactFile{Path: "/workspace/chat/a.jpg", Mime: "image/jpeg", Presentation: "document"}, ""},
		{"audio MIME never selects voice", artifactFile{Path: "/workspace/chat/a.ogg", Mime: "audio/ogg", Caption: "voice"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parts, err := artifactParts(artifactRequest{Text: "x", Files: []artifactFile{c.file}}, id)
			if err != nil {
				t.Fatal(err)
			}
			var got *store.DeliveryPart
			for i := range parts {
				if parts[i].Kind == store.PartUpload {
					got = &parts[i]
				}
			}
			if got == nil {
				t.Fatal("no upload part")
			}
			if got.ParseMode != c.want || got.ParseMode == store.UploadPresentationVoice {
				t.Fatalf("ParseMode = %q, want %q", got.ParseMode, c.want)
			}
		})
	}
}

func TestAgentRequestedVoiceStaysUnavailable(t *testing.T) {
	_, err := parseArtifactRequest([]byte(`{"version":1,"requestId":"abcdefgh12","capability":"c","text":"hi","voice":true}`))
	if code, ok := errCode(err); !ok || code != codeVoiceNA {
		t.Fatalf("err = %v", err)
	}
}

// CP-040 validation of the "as" field: unknown values are invalid like any
// unknown field, as:"photo" on a non-image is unsupported_type, as and the
// legacy presentation are mutually exclusive, and a file request without a
// capability says artifacts_disabled.
func TestParseArtifactRequestAsField(t *testing.T) {
	const head = `{"version":1,"requestId":"abcdefgh12","capability":"c","files":[`
	cases := []struct{ name, body, code string }{
		{"auto", head + `{"path":"/workspace/chat/a.pdf","as":"auto"}]}`, ""},
		{"document", head + `{"path":"/workspace/chat/a.png","as":"document"}]}`, ""},
		{"photo on png", head + `{"path":"/workspace/chat/a.png","as":"photo"}]}`, ""},
		{"photo on jpeg mime", head + `{"path":"/workspace/chat/scan","mime":"image/jpeg","as":"photo"}]}`, ""},
		{"photo on pdf", head + `{"path":"/workspace/chat/a.pdf","as":"photo"}]}`, codeUnsupportedType},
		{"photo on gif", head + `{"path":"/workspace/chat/a.gif","as":"photo"}]}`, codeUnsupportedType},
		{"unknown value", head + `{"path":"/workspace/chat/a.pdf","as":"video"}]}`, codeInvalidRequest},
		{"case matters", head + `{"path":"/workspace/chat/a.pdf","as":"Document"}]}`, codeInvalidRequest},
		{"as with presentation", head + `{"path":"/workspace/chat/a.png","as":"photo","presentation":"photo"}]}`, codeInvalidRequest},
		{"unknown field next to as", head + `{"path":"/workspace/chat/a.pdf","as":"auto","kind":"pdf"}]}`, codeInvalidRequest},
		{"no capability with files", `{"version":1,"requestId":"abcdefgh12","files":[{"path":"/workspace/chat/a.pdf"}]}`, codeArtifactsDisabled},
		{"no capability text only", `{"version":1,"requestId":"abcdefgh12","text":"hi"}`, codeInvalidRequest},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseArtifactRequest([]byte(c.body))
			if c.code == "" {
				if err != nil {
					t.Fatalf("err = %v", err)
				}
				return
			}
			if code, ok := errCode(err); !ok || code != c.code {
				t.Fatalf("err = %v, want %s", err, c.code)
			}
		})
	}
}
