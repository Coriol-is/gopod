package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// transcribeVoice downloads a Telegram voice message and transcribes
// it via the OpenAI Whisper API. Returns the transcribed text.
//
// OpenAI's /v1/audio/transcriptions accepts .ogg (which is what
// Telegram voice messages use), so no format conversion is needed.
func (b *Bot) transcribeVoice(ctx context.Context, voice *models.Voice) (string, error) {
	if voice == nil {
		return "", fmt.Errorf("nil voice")
	}
	return b.transcribeFile(ctx, voice.FileID, "voice.ogg")
}

// transcribeAudio transcribes the audio file or the video note m
// carries. Whisper takes mp3, m4a, ogg, wav, webm and mp4 (a video
// note) as they are, telling them apart by the file name's extension.
func (b *Bot) transcribeAudio(ctx context.Context, m *models.Message) (string, error) {
	switch {
	case m.Audio != nil:
		name := m.Audio.FileName
		if name == "" {
			name = "audio"
		}
		return b.transcribeFile(ctx, m.Audio.FileID, name)
	case m.VideoNote != nil:
		return b.transcribeFile(ctx, m.VideoNote.FileID, "video_note")
	}
	return "", fmt.Errorf("no audio in message")
}

// transcribeFile downloads a Telegram file and transcribes it. name is
// the file name Whisper sees; without an extension it gets the
// downloaded file's, which tells Whisper the format.
func (b *Bot) transcribeFile(ctx context.Context, fileID, name string) (string, error) {
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY not set (required for transcription)")
	}

	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", fmt.Errorf("GetFile: %w", err)
	}
	if filepath.Ext(name) == "" {
		name += filepath.Ext(file.FilePath)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api.FileDownloadLink(file), nil)
	if err != nil {
		return "", fmt.Errorf("download audio: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("download audio: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download audio: HTTP %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read audio data: %w", err)
	}

	b.log.Debug("audio downloaded",
		slog.String("file_id", fileID),
		slog.String("name", name),
		slog.Int("size", len(data)))

	url := b.whisperURL
	if url == "" {
		url = whisperDefaultURL
	}
	return whisperTranscribe(ctx, url, apiKey, data, name)
}

// whisperDefaultURL is OpenAI's transcription endpoint.
const whisperDefaultURL = "https://api.openai.com/v1/audio/transcriptions"

// whisperTranscribe calls a /v1/audio/transcriptions endpoint at url.
func whisperTranscribe(ctx context.Context, url, apiKey string, audioData []byte, filename string) (string, error) {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	// Add the audio file.
	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", fmt.Errorf("create form file: %w", err)
	}
	if _, err := part.Write(audioData); err != nil {
		return "", fmt.Errorf("write audio: %w", err)
	}

	// Add model field.
	writer.WriteField("model", "whisper-1")

	if err := writer.Close(); err != nil {
		return "", fmt.Errorf("close multipart: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, &body)
	if err != nil {
		return "", fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("whisper request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read whisper response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("whisper API returned %d: %s",
			resp.StatusCode, truncateStr(string(respBody), 200))
	}

	var result struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("parse whisper response: %w", err)
	}

	return result.Text, nil
}

// textToSpeech calls OpenAI's /v1/audio/speech endpoint and returns
// the audio data as opus-encoded bytes (Telegram's preferred format
// for voice messages).
func textToSpeech(ctx context.Context, apiKey, text string) ([]byte, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("OPENAI_API_KEY not set")
	}
	// Truncate for TTS: OpenAI TTS has a 4096 char limit.
	if len(text) > 4000 {
		text = text[:4000]
	}

	reqBody, _ := json.Marshal(map[string]any{
		"model": "tts-1",
		"input": text,
		"voice": "alloy",
		"response_format": "opus",
	})

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/audio/speech",
		bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("tts request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, &ttsStatusError{Status: resp.StatusCode, Body: truncateStr(string(body), 200)}
	}

	return io.ReadAll(resp.Body)
}

// ttsStatusError is a non-200 answer of the speech endpoint. Its text is the
// legacy message; the status lets the prepared path tell an explicit refusal
// from an ambiguous failure.
type ttsStatusError struct {
	Status int
	Body   string
}

func (e *ttsStatusError) Error() string {
	return fmt.Sprintf("tts API returned %d: %s", e.Status, e.Body)
}

// sendVoiceReply sends audio data as a Telegram voice message into
// thread (0 = main chat).
func (b *Bot) sendVoiceReply(ctx context.Context, chatID int64, thread int, replyToMsgID int, audioData []byte) error {
	params := &bot.SendVoiceParams{
		ChatID:          chatID,
		MessageThreadID: thread,
		Voice: &models.InputFileUpload{
			Filename: "reply.ogg",
			Data:     bytes.NewReader(audioData),
		},
	}
	if replyToMsgID > 0 {
		params.ReplyParameters = &models.ReplyParameters{
			MessageID:                replyToMsgID,
			AllowSendingWithoutReply: true,
		}
	}
	if b.threadGone(chatID, thread) {
		// The topic is gone; the text reply went to main, so does this.
		params.MessageThreadID = 0
		params.ReplyParameters = nil
	}
	_, err := b.api.SendVoice(ctx, params)
	// Not retried to main: the upload reader is spent, and the text
	// reply carries the answer. Marking keeps later sends out of the topic.
	b.checkThreadGone(chatID, params.MessageThreadID, err)
	return err
}

// truncateStr helper.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
