package store

import (
	"errors"
	"strings"
	"testing"
)

func TestCaptionUTF16Units(t *testing.T) {
	cases := []struct {
		s    string
		want int
	}{
		{"", 0},
		{"abc", 3},
		{"привет", 6},     // BMP: one unit each
		{"€", 1},          // euro sign, BMP
		{"\U0001F600", 2}, // emoji: surrogate pair
		{"a\U0001F600b", 4},
	}
	for _, c := range cases {
		if got := CaptionUTF16Units(c.s); got != c.want {
			t.Errorf("CaptionUTF16Units(%q) = %d, want %d", c.s, got, c.want)
		}
	}
}

// Telegram bounds a caption at 1024 UTF-16 units; the outbox must not accept a
// plan the transport can only get refused.
func TestUploadCaptionUnitBound(t *testing.T) {
	upload := func(caption string) PublishRequest {
		return PublishRequest{Parts: []DeliveryPart{{Kind: PartUpload, ArtifactRef: "a", Payload: []byte(caption)}}}
	}
	rejected := map[string]string{
		"513 emoji (1026 units, 2052 bytes)": strings.Repeat("\U0001F600", 513),
		"600 emoji":                          strings.Repeat("\U0001F600", 600),
		"1024 emoji":                         strings.Repeat("\U0001F600", 1024),
		"1025 ASCII":                         strings.Repeat("a", 1025),
	}
	accepted := map[string]string{
		"512 emoji (1024 units)":     strings.Repeat("\U0001F600", 512),
		"1024 ASCII":                 strings.Repeat("a", 1024),
		"1024 Cyrillic (2048 bytes)": strings.Repeat("я", 1024),
		"empty":                      "",
	}
	f := newDeliveryFixture(t)
	for name, caption := range rejected {
		req := upload(caption)
		req.Conversation = f.conv
		req.Owner = OwnerLegacyAgentIPC
		if err := validatePublish(req, 0); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("%s: err = %v, want ErrInvalidDelivery", name, err)
		}
	}
	for name, caption := range accepted {
		req := upload(caption)
		req.Conversation = f.conv
		req.Owner = OwnerLegacyAgentIPC
		if err := validatePublish(req, 0); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// The bound holds on the real publish path too, not only in the validator.
func TestPublishWithArtifactsRejectsAnEmojiCaptionOverTheUnitBound(t *testing.T) {
	f := newDeliveryFixture(t)
	p := f.reserve(1, 100)
	f.ready(p, 10)
	req := PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: []DeliveryPart{
		{Kind: PartUpload, ArtifactRef: ArtifactRef(p.ArtifactIDs[0]), Payload: []byte(strings.Repeat("\U0001F600", 600))}}}
	if _, err := f.st.PublishDeliveryWithArtifacts(t.Context(), req, p.ID); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("err = %v, want ErrInvalidDelivery", err)
	}
}
