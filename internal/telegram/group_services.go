package telegram

import (
	"encoding/json"
	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/go-telegram/bot/models"
)

const groupSpeechUnavailable = "Voice and audio processing is unavailable in shared chats. Send a text message instead."

// Speech requires the trusted Telegram private context. A positive actor ID or
// an owner flag cannot turn a group/channel message into a personal service use.
func sharedSpeechInput(m *models.Message) bool {
	return m != nil && (m.Voice != nil || m.Audio != nil || m.VideoNote != nil) && !privateLoginMessage(m)
}

// sharedSensitiveInput covers Telegram-provided message metadata before either
// raw history or normalized queue history can retain it. JSON is inspected only
// in memory: hidden entity/button URLs and external reply metadata are included,
// while attachment bytes are not downloaded or scanned here.
func sharedSensitiveInput(m *models.Message) bool {
	if m == nil || privateLoginMessage(m) {
		return false
	}
	if memory.IsGroupSensitiveText(m.Text) || memory.IsGroupSensitiveText(m.Caption) || memory.IsGroupSensitiveText(inboundContext(m)) {
		return true
	}
	// Raw persistence keeps the complete replied text, even when the normalized
	// prompt truncates it or prefers an explicit quote; inspect it independently.
	if r := m.ReplyToMessage; r != nil {
		if memory.IsGroupSensitiveText(r.Text) || memory.IsGroupSensitiveText(r.Caption) {
			return true
		}
	}
	raw, err := json.Marshal(m)
	return err != nil || memory.IsGroupSensitiveText(string(raw))
}
