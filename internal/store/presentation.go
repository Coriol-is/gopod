package store

import "context"

// UploadPresentationVoice is carried in the ParseMode field of an upload part
// (an upload has no parse mode) to ask the transport for a Telegram voice
// message instead of a document. It is set only by the host when it builds a
// plan for audio it generated itself; the strict V1 agent request has no field
// that reaches it, so an agent-supplied file is always a document.
const UploadPresentationVoice = "voice"

// UploadPresentationPhoto asks the transport to try sendPhoto and to send the
// same snapshot bytes as a document when the photo cannot be sent as one. It is
// only a hint (the document is always a safe outcome), so unlike voice it needs
// no host-only publish path. Nothing in the store sets it yet; the agent-request
// route that does is a separate step.
const UploadPresentationPhoto = "photo"

// PublishVoiceDeliveryWithArtifacts is the only way to publish an upload part
// with UploadPresentationVoice; every other publish path rejects the marker.
// Callers are host code that generated the audio itself.
func (s *Store) PublishVoiceDeliveryWithArtifacts(ctx context.Context, req PublishRequest, prep string) (Delivery, error) {
	req.voicePresentation = true
	return s.PublishDeliveryWithArtifacts(ctx, req, prep)
}
