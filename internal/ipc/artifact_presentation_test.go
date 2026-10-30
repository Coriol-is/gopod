package ipc

import (
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// The request presentation is a hint that reaches the part: photo maps to the
// photo marker, document and empty stay empty, and nothing an agent writes (a
// MIME claim, an unknown value) can select the host-only voice marker.
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
		{"empty stays a document", artifactFile{Path: "/workspace/chat/a.png", Mime: "image/png"}, ""},
		{"image MIME alone is not a photo", artifactFile{Path: "/workspace/chat/a.jpg", Mime: "image/jpeg"}, ""},
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
