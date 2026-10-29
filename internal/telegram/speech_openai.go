package telegram

import (
	"context"
	"errors"
	"net/http"
	"os"
)

// SpeechBackendOpenAI is the backend label of the existing personal TTS call
// (OpenAI /v1/audio/speech, model tts-1, voice alloy, opus).
const SpeechBackendOpenAI = "openai-tts-1"

// PersonalSpeechCredential is the credential reference of the owner's own
// OpenAI account. It is resolved only for a grant that names it.
const PersonalSpeechCredential = "personal_openai"

// openAISpeech adapts the legacy textToSpeech call to SpeechBackend. The call is
// unchanged (same model, voice, 4000-byte truncation); only the classification
// of its failure is new.
type openAISpeech struct{}

// NewOpenAISpeechBackend returns the OpenAI backend for the existing voice reply.
func NewOpenAISpeechBackend() SpeechBackend { return openAISpeech{} }

func (openAISpeech) Name() string { return SpeechBackendOpenAI }

func (openAISpeech) Synthesize(ctx context.Context, credential, text string) ([]byte, error) {
	audio, err := textToSpeech(ctx, credential, text)
	if err == nil {
		return audio, nil
	}
	return nil, classifyTTSError(err)
}

// classifyTTSError maps a failed speech call to an explicit refusal when the
// status proves the request was rejected, and leaves anything else ambiguous.
func classifyTTSError(err error) error {
	var se *ttsStatusError
	if errors.As(err, &se) {
		switch se.Status {
		case http.StatusUnauthorized, http.StatusForbidden:
			return &SpeechRefusal{Code: "backend_credentials"}
		case http.StatusTooManyRequests:
			return &SpeechRefusal{Code: "backend_quota"}
		case http.StatusBadRequest, http.StatusUnprocessableEntity:
			return &SpeechRefusal{Code: "backend_rejected"}
		}
	}
	// Timeouts, 5xx, connection loss and anything unrecognized: the request may
	// have been accepted and billed.
	return err
}

// envSpeechCredentials resolves PersonalSpeechCredential from the process
// environment. It is the only place the personal key is read for the prepared
// path, and only after a grant named the reference.
type envSpeechCredentials struct{}

// NewEnvSpeechCredentials returns the environment-backed credential resolver.
func NewEnvSpeechCredentials() SpeechCredentials { return envSpeechCredentials{} }

func (envSpeechCredentials) Credential(ref string) string {
	if ref == PersonalSpeechCredential {
		return os.Getenv("OPENAI_API_KEY")
	}
	return ""
}
