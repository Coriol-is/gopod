package ipc

import (
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// An agent-supplied file is always a document: no request field may reach the
// host-only voice presentation of an upload part, whatever MIME the agent claims.
func TestAgentUploadPartsNeverCarryAPresentation(t *testing.T) {
	req := artifactRequest{Text: "x", Files: []artifactFile{{Path: "/workspace/chat/a.ogg", Mime: "audio/ogg", Caption: "voice"}}}
	parts, err := artifactParts(req, []string{"11111111-1111-4111-8111-111111111111"})
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range parts {
		if p.Kind == store.PartUpload && (p.ParseMode != "" || p.ParseMode == store.UploadPresentationVoice) {
			t.Fatalf("upload part carries presentation %q", p.ParseMode)
		}
	}
}

func TestAgentRequestedVoiceStaysUnavailable(t *testing.T) {
	_, err := parseArtifactRequest([]byte(`{"version":1,"requestId":"abcdefgh12","capability":"c","text":"hi","voice":true}`))
	if code, ok := errCode(err); !ok || code != codeVoiceNA {
		t.Fatalf("err = %v", err)
	}
}
