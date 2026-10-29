package telegram

// One immutable delivery plan for a spoken reply (CP-003 PR 5).
//
// The source text parts and the generated audio are published together in a
// single delivery group, so the host's own durable state, not a log line, says
// which half reached the user. Foreground use waits for PR 6 (foreground freeze
// and cutover); until then this builds and tests the plan.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/Coriol-is/gopod/internal/store"
)

// VoiceReplyPlan is the frozen content of one spoken reply.
type VoiceReplyPlan struct {
	Conversation store.ConversationScope
	Owner        store.DeliveryOwner
	ResultID     string // required with OwnerControllerResult, empty otherwise
	ActorRef     string
	TaskRef      string
	RunRef       string
	// Text is the exact rendered text parts, sent before the audio. It is empty
	// for a voice-only reply.
	Text []store.DeliveryPart
	// Speech is the ready clip; it must come from SpeechPreparer.Prepare.
	Speech PreparedSpeech
	// IntentKey is the key the clip was prepared under; it is also the request
	// identity, so publishing twice converges on one delivery and one upload.
	IntentKey string
}

// VoicePublisher is the part of the store that publishes a plan with its artifact.
type VoicePublisher interface {
	PublishDeliveryWithArtifacts(ctx context.Context, req store.PublishRequest, prep string) (store.Delivery, error)
}

// PublishRequest builds the store request: text parts first, then one upload
// part with the voice presentation. The request identity binds the key to the
// text bytes and the audio hash, so the same key with different content is a
// conflict rather than a second send.
func (p VoiceReplyPlan) PublishRequest() (store.PublishRequest, error) {
	if p.IntentKey == "" || p.Speech.Ref == "" || p.Speech.SHA256 == "" {
		return store.PublishRequest{}, fmt.Errorf("%w: voice plan needs a prepared clip and an intent key", store.ErrInvalidDelivery)
	}
	parts := make([]store.DeliveryPart, 0, len(p.Text)+1)
	h := sha256.New()
	for _, t := range p.Text {
		if t.Kind != store.PartText {
			return store.PublishRequest{}, fmt.Errorf("%w: a voice plan carries text parts only", store.ErrInvalidDelivery)
		}
		parts = append(parts, t)
		h.Write([]byte(strconv.Itoa(len(t.Payload)) + ":"))
		h.Write(t.Payload)
		h.Write([]byte(t.ParseMode + ";"))
	}
	h.Write([]byte("audio:" + p.Speech.SHA256))
	parts = append(parts, store.DeliveryPart{Kind: store.PartUpload, ArtifactRef: p.Speech.Ref, ParseMode: store.UploadPresentationVoice})
	return store.PublishRequest{
		Conversation: p.Conversation,
		ResultID:     p.ResultID,
		Owner:        p.Owner,
		ActorRef:     p.ActorRef,
		TaskRef:      p.TaskRef,
		RunRef:       p.RunRef,
		Parts:        parts,
		Request:      &store.RequestIdentity{Key: p.IntentKey, PayloadHash: hex.EncodeToString(h.Sum(nil))},
	}, nil
}

// PublishVoiceReply publishes the plan in one transaction. A repeat of the same
// key and content returns the first delivery (Created=false) and never a second
// upload part; a prepared clip that can no longer be published returns the
// store error, and the audio is not regenerated.
func PublishVoiceReply(ctx context.Context, pub VoicePublisher, plan VoiceReplyPlan) (store.Delivery, error) {
	req, err := plan.PublishRequest()
	if err != nil {
		return store.Delivery{}, err
	}
	if plan.Speech.PreparationID == "" {
		return store.Delivery{}, errors.New("voice plan: clip has no preparation")
	}
	return pub.PublishDeliveryWithArtifacts(ctx, req, plan.Speech.PreparationID)
}

// VoiceDeliveryStatus is the truthful, user-facing state of a spoken reply.
// "uncertain" is never reported as sent or as failed.
type VoiceDeliveryStatus struct {
	Text     string // none, pending, sent, partial, uncertain, failed
	Voice    string // none, pending, sent, uncertain, failed
	Complete bool   // every part is resolved (no pending, sending or uncertain)
	Summary  string
}

// DescribeVoiceDelivery derives the status from the stored parts.
func DescribeVoiceDelivery(d store.Delivery) VoiceDeliveryStatus {
	var text, voice []string
	for _, part := range d.Parts {
		st := part.State
		if part.Part.Kind == store.PartUpload {
			voice = append(voice, st)
		} else {
			text = append(text, st)
		}
	}
	s := VoiceDeliveryStatus{Text: foldParts(text), Voice: foldParts(voice), Complete: !d.Unresolved}
	switch {
	case s.Text == "sent" && s.Voice == "sent":
		s.Summary = "text and voice delivered"
	case s.Text == "none" && s.Voice == "sent":
		s.Summary = "voice delivered"
	case s.Voice == "uncertain" && s.Text == "sent":
		s.Summary = "text delivered; voice uncertain (not resent)"
	case s.Voice == "uncertain":
		s.Summary = "voice uncertain (not resent)"
	case s.Text == "uncertain":
		s.Summary = "text uncertain (not resent)"
	case s.Text == "sent" && s.Voice == "failed":
		s.Summary = "text delivered; voice not delivered"
	case s.Text == "sent" && s.Voice == "pending":
		s.Summary = "text delivered; voice pending"
	case s.Text == "failed" && s.Voice == "sent":
		s.Summary = "voice delivered; text not delivered"
	default:
		s.Summary = "text " + s.Text + "; voice " + s.Voice
	}
	return s
}

// foldParts reduces part states of one kind to one word; the worst unresolved
// or uncertain state wins, so nothing uncertain reads as delivered.
func foldParts(states []string) string {
	if len(states) == 0 {
		return "none"
	}
	count := map[string]int{}
	for _, s := range states {
		count[s]++
	}
	switch {
	case count[store.PartUncertain] > 0:
		return "uncertain"
	case count[store.PartSending] > 0, count[store.PartPending] > 0:
		if count[store.PartSent] > 0 {
			return "partial"
		}
		return "pending"
	case count[store.PartSent] == len(states):
		return "sent"
	case count[store.PartSent] > 0:
		return "partial"
	}
	return "failed"
}
