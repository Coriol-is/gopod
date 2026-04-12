package telegram

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// downloadPhoto downloads the highest-resolution photo from a Telegram
// message and saves it to the chat's uploads directory. Returns the
// host-side path and the container-side path (/workspace/chat/uploads/...).
//
// Telegram sends photos as an array of PhotoSize (thumbnails + original);
// we pick the last one (highest resolution).
func (b *Bot) downloadPhoto(ctx context.Context, chatFolder string, photos []models.PhotoSize) (hostPath, containerPath string, err error) {
	if len(photos) == 0 {
		return "", "", fmt.Errorf("no photos in message")
	}
	// Last element = highest resolution.
	best := photos[len(photos)-1]

	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: best.FileID})
	if err != nil {
		return "", "", fmt.Errorf("GetFile: %w", err)
	}

	downloadURL := b.api.FileDownloadLink(file)

	// Download the file.
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	// Save to chats/<folder>/uploads/<timestamp>.<ext>
	ext := filepath.Ext(file.FilePath)
	if ext == "" {
		ext = ".jpg"
	}
	filename := fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext)

	uploadsDir := filepath.Join(b.chatDir(chatFolder), "uploads")
	if err := os.MkdirAll(uploadsDir, 0o755); err != nil {
		return "", "", fmt.Errorf("mkdir uploads: %w", err)
	}

	hostPath = filepath.Join(uploadsDir, filename)
	f, err := os.Create(hostPath)
	if err != nil {
		return "", "", fmt.Errorf("create file: %w", err)
	}
	defer f.Close()

	if _, err := io.Copy(f, resp.Body); err != nil {
		return "", "", fmt.Errorf("save file: %w", err)
	}

	containerPath = "/workspace/chat/uploads/" + filename

	b.log.Debug("downloaded photo",
		slog.String("file_id", best.FileID),
		slog.String("host_path", hostPath),
		slog.String("container_path", containerPath),
		slog.Int("width", best.Width),
		slog.Int("height", best.Height))

	return hostPath, containerPath, nil
}

// downloadDocument downloads a document/file attachment. Same pattern
// as downloadPhoto but for arbitrary file types.
func (b *Bot) downloadDocument(ctx context.Context, chatFolder string, doc *models.Document) (hostPath, containerPath string, err error) {
	if doc == nil {
		return "", "", fmt.Errorf("nil document")
	}

	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: doc.FileID})
	if err != nil {
		return "", "", fmt.Errorf("GetFile: %w", err)
	}

	downloadURL := b.api.FileDownloadLink(file)
	resp, err := http.Get(downloadURL)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()

	filename := doc.FileName
	if filename == "" {
		ext := filepath.Ext(file.FilePath)
		if ext == "" {
			ext = ".bin"
		}
		filename = fmt.Sprintf("%d%s", time.Now().UnixMilli(), ext)
	}

	uploadsDir := filepath.Join(b.chatDir(chatFolder), "uploads")
	os.MkdirAll(uploadsDir, 0o755)

	hostPath = filepath.Join(uploadsDir, filename)
	f, err := os.Create(hostPath)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	io.Copy(f, resp.Body)

	containerPath = "/workspace/chat/uploads/" + filename
	return hostPath, containerPath, nil
}

// chatDir returns the host-side chat directory path.
func (b *Bot) chatDir(chatFolder string) string {
	if b.runner != nil {
		// Use the runner's configured ChatsDir.
		return filepath.Join(b.runner.ChatsDir(), chatFolder)
	}
	// Fallback: relative path.
	return filepath.Join("chats", chatFolder)
}
