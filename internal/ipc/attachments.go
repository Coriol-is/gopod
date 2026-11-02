package ipc

// Honest refusal for agent attachments (CP-040). A V1 request that carries
// files and is refused ends in a .failed receipt with a sanitized code, as
// every V1 rejection does, and additionally in one plain-text notice to the
// chat the IPC folder belongs to, so the user sees which file was not attached
// and why instead of a reply that silently lacks it.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
)

const (
	maxNoticePathBytes = 200
	maxNoticeFiles     = maxArtifactFiles
	noticeSendTimeout  = 10 * time.Second
)

// attachmentHelp is the receipt help for a refused attachment.
const attachmentHelp = "The file was NOT attached. Read /workspace/chat/ATTACHMENTS.md. Tell the user the file stays at its path " +
	"and could not be attached, with this reason; do not claim it was sent. Retry only after fixing the cause, with a new filename and requestId."

// attachmentReasons are the Russian user-facing explanations of refusal codes.
// A code without an entry is shown as is.
var attachmentReasons = map[string]string{
	codeArtifactsDisabled:    "отправка файлов в этом чате выключена",
	codePathOutside:          "путь вне /workspace/chat",
	codePathDenied:           "путь запрещён (скрытый, служебный или защищённый файл)",
	codeUnsupportedType:      "как фото можно отправить только PNG или JPEG",
	codeFileTooLarge:         "файл больше лимита (20 МиБ на файл, 40 МиБ на запрос)",
	codeEmptyFile:            "файл пустой",
	codeSourceMissing:        "файл не найден",
	codeSourceChanged:        "файл менялся во время копирования",
	codeNotRegularFile:       "это не обычный файл (ссылка, папка или устройство)",
	codeHardlinkDenied:       "у файла есть жёсткие ссылки",
	codeStaleCapability:      "разрешение этого запуска истекло",
	codeQuotaExceeded:        "превышена квота хранилища файлов",
	codeStorageFull:          "на сервере мало места",
	codeRateLimited:          "слишком много файлов за короткое время",
	codeRecipientUnavailable: "этот чат не может получать файлы",
	codeInvalidRequest:       "неверный запрос",
	codeRequestExpired:       "запрос устарел",
	codeTooManyParts:         "слишком много частей в одном сообщении",
}

// attachmentCodes are the rejection codes whose receipt gets attachmentHelp.
func isAttachmentCode(code string) bool {
	_, ok := attachmentReasons[code]
	return ok && code != codeInvalidRequest && code != codeRecipientUnavailable && code != codeRequestExpired && code != codeTooManyParts
}

// refuseArtifact persists a rejection of an artifact-route request and, once
// the request reached its terminal state, tells the user about its files.
func (w *Watcher) refuseArtifact(ctx context.Context, src store.ScopeRef, folder, filePath string, id store.RequestIdentity, code string, files []artifactFile) bool {
	done := w.rejectDurable(ctx, src, folder, filePath, id, code)
	if done && len(files) > 0 {
		w.noticeAttachmentRefused(ctx, folder, files, code)
	}
	return done
}

// v1FilePaths returns the file paths of a request that declares a version and
// carries at least one file, or nil. It is lenient on purpose: it only decides
// that the request is an attachment request, never that it is valid.
func v1FilePaths(raw []byte) []string {
	if !isArtifactVersion(raw) {
		return nil
	}
	var probe struct {
		Files []json.RawMessage `json:"files"`
	}
	if json.Unmarshal(raw, &probe) != nil || len(probe.Files) == 0 {
		return nil
	}
	paths := make([]string, 0, len(probe.Files))
	for _, f := range probe.Files {
		var p struct {
			Path string `json:"path"`
		}
		dec := json.NewDecoder(bytes.NewReader(f))
		if dec.Decode(&p) != nil {
			p.Path = ""
		}
		paths = append(paths, p.Path)
	}
	return paths
}

// refuseDisabled ends a V1 file request that cannot reach the artifact route
// (GOPOD_OUTBOX_ARTIFACTS=0, the durable route off, or a folder other than the
// owner's) with artifacts_disabled. Nothing of the request is sent: a text
// carried next to the files is not delivered without them.
func (w *Watcher) refuseDisabled(ctx context.Context, folder, filePath string, paths []string) bool {
	done := w.finishRejected(folder, filePath, codeArtifactsDisabled)
	if done {
		files := make([]artifactFile, len(paths))
		for i, p := range paths {
			files[i].Path = p
		}
		w.noticeAttachmentRefused(ctx, folder, files, codeArtifactsDisabled)
	}
	return done
}

// noticeAttachmentRefused sends one best-effort plain-text notice to the chat
// the folder is registered to. The destination comes from the folder (path
// authorization), never from the request. A failed notice is logged; the
// receipt stays the authority.
func (w *Watcher) noticeAttachmentRefused(ctx context.Context, folder string, files []artifactFile, code string) {
	if w.msgs == nil || w.resolve == nil {
		return
	}
	jid, ok := w.resolve(folder)
	if !ok || jid == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, noticeSendTimeout)
	defer cancel()
	if err := w.msgs.Send(ctx, jid, attachmentNotice(files, code)); err != nil {
		w.log.Warn("ipc: attachment refusal notice not sent", "folder", folder, "code", code, "err", err)
	}
}

// attachmentNotice renders the user-facing refusal text.
func attachmentNotice(files []artifactFile, code string) string {
	reason, ok := attachmentReasons[code]
	if !ok {
		reason = "отказ"
	}
	var b strings.Builder
	if len(files) == 1 {
		b.WriteString("Не удалось приложить файл ")
		b.WriteString(displayPath(files[0].Path))
	} else {
		b.WriteString("Не удалось приложить файлы:")
		for i, f := range files {
			if i == maxNoticeFiles {
				fmt.Fprintf(&b, "\n… и ещё %d", len(files)-maxNoticeFiles)
				break
			}
			b.WriteString("\n• ")
			b.WriteString(displayPath(f.Path))
		}
	}
	fmt.Fprintf(&b, "\nПричина: %s (%s).", reason, code)
	b.WriteString("\nФайл не отправлен и остался в рабочей папке чата.")
	return b.String()
}

// displayPath makes an agent-supplied path safe to show: no control or bidi
// characters, bounded length. It is display only, never resolved.
func displayPath(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r),
			r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0xFEFF:
			continue
		}
		if b.Len()+utf8.RuneLen(r) > maxNoticePathBytes {
			b.WriteString("…")
			break
		}
		b.WriteRune(r)
	}
	if b.Len() == 0 {
		return "(без пути)"
	}
	return b.String()
}
