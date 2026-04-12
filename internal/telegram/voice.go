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

	// Download the voice file from Telegram.
	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: voice.FileID})
	if err != nil {
		return "", fmt.Errorf("GetFile: %w", err)
	}

	downloadURL := b.api.FileDownloadLink(file)
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", fmt.Errorf("download voice: %w", err)
	}
	defer resp.Body.Close()

	voiceData, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read voice data: %w", err)
	}

	b.log.Debug("voice downloaded",
		slog.String("file_id", voice.FileID),
		slog.Int("duration", voice.Duration),
		slog.Int("size", len(voiceData)))

	// Send to OpenAI Whisper API.
	apiKey := os.Getenv("OPENAI_API_KEY")
	if apiKey == "" {
		return "", fmt.Errorf("OPENAI_API_KEY not set (required for voice transcription)")
	}

	return whisperTranscribe(ctx, apiKey, voiceData, "voice.ogg")
}

// whisperTranscribe calls OpenAI's /v1/audio/transcriptions endpoint.
func whisperTranscribe(ctx context.Context, apiKey string, audioData []byte, filename string) (string, error) {
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

	req, err := http.NewRequestWithContext(ctx, "POST",
		"https://api.openai.com/v1/audio/transcriptions", &body)
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
		return nil, fmt.Errorf("tts API returned %d: %s", resp.StatusCode, truncateStr(string(body), 200))
	}

	return io.ReadAll(resp.Body)
}

// sendVoiceReply sends audio data as a Telegram voice message.
func (b *Bot) sendVoiceReply(ctx context.Context, chatID int64, replyToMsgID int, audioData []byte) error {
	params := &bot.SendVoiceParams{
		ChatID: chatID,
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
	_, err := b.api.SendVoice(ctx, params)
	return err
}

// truncateStr helper.
func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
