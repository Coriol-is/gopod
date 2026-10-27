package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"

	"github.com/go-telegram/bot"
	"github.com/go-telegram/bot/models"
)

// uploadSeq numbers downloaded files within the process, so files
// downloaded within one millisecond (an album) never share a name.
var uploadSeq atomic.Int64

// mediaDownloadTimeout bounds one inbound media download. Downloads run
// detached from the bot's context (context.WithoutCancel) so a shutdown
// mid-download still ends in a queued turn (D020); this keeps them from
// holding the shutdown forever.
const mediaDownloadTimeout = 2 * time.Minute

// downloadContext is the context inbound media downloads run in: not
// cancelled by shutdown, bounded by mediaDownloadTimeout.
func downloadContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), mediaDownloadTimeout)
}

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
	hostPath, containerPath, err = b.saveUpload(ctx, chatFolder, best.FileID, "", ".jpg")
	if err != nil {
		return "", "", err
	}
	b.log.Debug("downloaded photo",
		slog.String("file_id", best.FileID),
		slog.String("host_path", hostPath),
		slog.String("container_path", containerPath),
		slog.Int("width", best.Width),
		slog.Int("height", best.Height))
	return hostPath, containerPath, nil
}

// downloadDocument downloads a document/file attachment. Same pattern
// as downloadPhoto; the saved name keeps the sender's file name (its
// last path element only) after a unique prefix.
func (b *Bot) downloadDocument(ctx context.Context, chatFolder string, doc *models.Document) (hostPath, containerPath string, err error) {
	if doc == nil {
		return "", "", fmt.Errorf("nil document")
	}
	return b.saveUpload(ctx, chatFolder, doc.FileID, doc.FileName, ".bin")
}

// saveUpload downloads a Telegram file into the chat's uploads
// directory as "<unix ms>-<seq>-<name>", or "<unix ms>-<seq><ext>" when
// name is empty or unusable, ext being the downloaded file's extension
// or defaultExt. The name comes from the sender, so only its last path
// element is used: it can never leave the chat's uploads directory.
func (b *Bot) saveUpload(ctx context.Context, chatFolder, fileID, name, defaultExt string) (hostPath, containerPath string, err error) {
	chatDir, err := b.chatDir(ctx, chatFolder)
	if err != nil {
		return "", "", err
	}
	file, err := b.api.GetFile(ctx, &bot.GetFileParams{FileID: fileID})
	if err != nil {
		return "", "", fmt.Errorf("GetFile: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.api.FileDownloadLink(file), nil)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("download: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", fmt.Errorf("download: HTTP %d", resp.StatusCode)
	}

	prefix := fmt.Sprintf("%d-%d", time.Now().UnixMilli(), uploadSeq.Add(1))
	filename := prefix + "-" + safeUploadName(name)
	if safeUploadName(name) == "" {
		ext := filepath.Ext(file.FilePath)
		if ext == "" {
			ext = defaultExt
		}
		filename = prefix + ext
	}

	if err = os.MkdirAll(chatDir, 0o755); err != nil {
		return "", "", fmt.Errorf("upload workspace unavailable")
	}
	root, err := os.OpenRoot(chatDir)
	if err != nil {
		return "", "", fmt.Errorf("upload workspace unavailable")
	}
	defer root.Close()
	if err = root.Mkdir("uploads", 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", "", fmt.Errorf("upload directory unavailable")
	}
	info, err := root.Lstat("uploads")
	if err != nil || !info.IsDir() {
		return "", "", fmt.Errorf("upload directory must be a regular directory")
	}
	relative := filepath.Join("uploads", filename)
	f, err := root.OpenFile(relative, os.O_WRONLY|os.O_CREATE|os.O_EXCL|syscall.O_NOFOLLOW, 0o600)
	if err != nil {
		return "", "", fmt.Errorf("upload file unavailable")
	}
	hostPath = filepath.Join(chatDir, relative)
	_, copyErr := io.Copy(f, resp.Body)
	closeErr := f.Close()
	if err := errors.Join(copyErr, closeErr); err != nil {
		_ = root.Remove(relative)
		return "", "", fmt.Errorf("save file: %w", err)
	}
	return hostPath, "/workspace/chat/uploads/" + filename, nil
}

// safeUploadName is the last path element of a sender-supplied file
// name, or "" when nothing usable is left ("", ".", "..", only
// separators). Backslashes count as separators too.
func safeUploadName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return '_'
		}
		return r
	}, name)
	if strings.Trim(name, "/") == "" {
		return ""
	}
	base := filepath.Base(name)
	if base == "." || base == ".." || strings.TrimSpace(base) == "" {
		return ""
	}
	return base
}

// chatDir returns the host-side chat directory path.
func (b *Bot) chatDir(ctx context.Context, chatFolder string) (string, error) {
	if b.runner != nil {
		return b.runner.ChatWorkspace(ctx, chatFolder)
	}
	return filepath.Join("chats", chatFolder), nil
}
