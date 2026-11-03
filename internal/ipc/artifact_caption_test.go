package ipc

import (
	"encoding/json"
	"strings"
	"testing"
)

func captionRequest(t *testing.T, caption string) []byte {
	t.Helper()
	b, err := json.Marshal(map[string]any{
		"version": 1, "requestId": "abcdefgh12", "capability": "c",
		"files": []map[string]any{{"path": "chart.png", "caption": caption}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Telegram counts captions in UTF-16 units, so the request validator must too:
// a 600-emoji caption is 600 runes but 1200 units and would be refused upstream.
func TestCaptionIsBoundedInUTF16Units(t *testing.T) {
	emoji := "\U0001F600"
	cases := []struct {
		name    string
		caption string
		ok      bool
	}{
		{"1024 ASCII", strings.Repeat("a", 1024), true},
		{"1025 ASCII", strings.Repeat("a", 1025), false},
		{"512 emoji is exactly 1024 units", strings.Repeat(emoji, 512), true},
		{"513 emoji", strings.Repeat(emoji, 513), false},
		{"600 emoji", strings.Repeat(emoji, 600), false},
		{"1024 emoji", strings.Repeat(emoji, 1024), false},
		{"1023 BMP letters and one emoji", strings.Repeat("я", 1022) + emoji, true},
		{"1023 BMP letters and one emoji over", strings.Repeat("я", 1023) + emoji, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := parseArtifactRequest(captionRequest(t, c.caption))
			if c.ok && err != nil {
				t.Fatalf("rejected: %v", err)
			}
			if !c.ok {
				if code, _ := errCode(err); code != codeInvalidRequest {
					t.Fatalf("err = %v, want %s", err, codeInvalidRequest)
				}
			}
		})
	}
}
