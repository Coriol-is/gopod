package telegram

import (
	"strconv"

	"github.com/go-telegram/bot/models"
)

// transcriptWithMetadata labels speech-to-text input using only Telegram's
// media type and duration. Never infer speaker, language, confidence or vocal
// cues from text, or include file ids/names in this trusted metadata header.
func transcriptWithMetadata(m *models.Message, transcript string) string {
	if m == nil || transcript == "" {
		return transcript
	}
	var source string
	var duration int
	switch {
	case m.Voice != nil:
		source, duration = "voice_message", m.Voice.Duration
	case m.Audio != nil:
		source, duration = "audio_file", m.Audio.Duration
	case m.VideoNote != nil:
		source, duration = "video_note", m.VideoNote.Duration
	default:
		return transcript
	}
	header := "[Telegram input metadata: source=" + source + "; transcription=automatic"
	if duration > 0 {
		header += "; duration_seconds=" + strconv.Itoa(duration)
	}
	return header + "]\n\n" + transcript
}
