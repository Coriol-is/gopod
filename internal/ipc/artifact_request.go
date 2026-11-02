package ipc

// Strict V1 request schema (docs/superpowers/specs/2026-10-26-immutable-artifact-delivery.md).
// Unknown fields are rejected. No field grants an owner role, recipient, privacy
// level or thread: those come from the host-issued capability.

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"path"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Coriol-is/gopod/internal/store"
)

// V1 bounds from the spec. The 64 KiB request limit is enforced by the reader.
const (
	maxArtifactText        = 48 << 10
	maxArtifactFiles       = 4
	maxCaptionUnits        = store.MaxUploadCaptionUnits // UTF-16 units, Telegram's count
	maxFilenameBytes       = 120
	maxMIMEBytes           = 127
	maxRequestIDBytes      = 64
	maxCapabilityTokenSize = 128
)

// Sanitized rejection codes for the artifact route. They are persisted and
// shown to the agent, so they never carry paths, raw errors or content.
const (
	codeStaleCapability = "stale_capability"
	codePathDenied      = "path_denied"
	codeNotRegularFile  = "not_regular_file"
	codeHardlinkDenied  = "hardlink_denied"
	codeFileTooLarge    = "file_too_large"
	codeEmptyFile       = "empty_file"
	codeSourceMissing   = "source_missing"
	codeSourceChanged   = "source_changed"
	codeStorageFull     = "storage_full"
	codeRateLimited     = "rate_limited"
	codeVoiceNA         = "voice_unavailable"
	codeTooManyParts    = "too_many_parts"
	codePreparationFail = "preparation_failed"

	// CP-040 agent attachment codes. artifacts_disabled: the request carries
	// files but this run has no artifact route or capability.
	// path_outside_workspace: the path is not a clean absolute path under
	// /workspace/chat. unsupported_type: as:"photo" on a file that is not named
	// or typed as PNG/JPEG.
	codeArtifactsDisabled = "artifacts_disabled"
	codePathOutside       = "path_outside_workspace"
	codeUnsupportedType   = "unsupported_type"
)

// artifactError carries a sanitized code for the receipt.
type artifactError struct{ code string }

func (e *artifactError) Error() string { return "ipc: artifact request rejected: " + e.code }

func reject(code string) error { return &artifactError{code: code} }

func errCode(err error) (string, bool) {
	var ae *artifactError
	if errors.As(err, &ae) {
		return ae.code, true
	}
	return "", false
}

type artifactFile struct {
	Path         string `json:"path"`
	Filename     string `json:"filename"`
	Mime         string `json:"mime"`
	Caption      string `json:"caption"`
	Presentation string `json:"presentation"`
	// As is the CP-040 agent-facing presentation: "auto" (default), "document"
	// or "photo". It and the older Presentation are mutually exclusive.
	As string `json:"as"`
}

type artifactRequest struct {
	Version    int            `json:"version"`
	RequestID  string         `json:"requestId"`
	Capability string         `json:"capability"`
	ChatJID    string         `json:"chatJid"`
	Text       string         `json:"text"`
	Files      []artifactFile `json:"files"`
	Voice      bool           `json:"voice"`
}

var (
	requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{7,63}$`)
	mimePattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9!#$&^_.+-]*/[a-z0-9][a-z0-9!#$&^_.+-]*$`)
)

// isArtifactVersion reports whether a decoded request declares a version, which
// is what routes it to the V1 parser. V0 requests have no version field.
func isArtifactVersion(raw []byte) bool {
	var probe struct {
		Version *int `json:"version"`
	}
	return json.Unmarshal(raw, &probe) == nil && probe.Version != nil
}

// parseArtifactRequest decodes and validates a V1 request. Any violation rejects
// the whole request.
func parseArtifactRequest(raw []byte) (artifactRequest, error) {
	var req artifactRequest
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		return req, reject(codeInvalidRequest)
	}
	if _, err := dec.Token(); err != io.EOF {
		return req, reject(codeInvalidRequest) // trailing data
	}
	switch {
	case req.Version != 1,
		!requestIDPattern.MatchString(req.RequestID),
		len(req.Capability) > maxCapabilityTokenSize,
		!utf8.ValidString(req.Text) || len(req.Text) > maxArtifactText:
		return req, reject(codeInvalidRequest)
	case req.Capability == "" && len(req.Files) > 0:
		// No GOPOD_ARTIFACT_CAP in this run: say so instead of "invalid".
		return req, reject(codeArtifactsDisabled)
	case req.Capability == "":
		return req, reject(codeInvalidRequest)
	}
	if strings.TrimSpace(req.Text) == "" && len(req.Files) == 0 && !req.Voice {
		return req, reject(codeInvalidRequest)
	}
	if req.Voice {
		if strings.TrimSpace(req.Text) == "" {
			return req, reject(codeInvalidRequest)
		}
		// Voice needs an approved backend, credentials and cost policy per
		// scope (PR 5). Until then it is refused, never silently dropped.
		return req, reject(codeVoiceNA)
	}
	if len(req.Files) > maxArtifactFiles {
		return req, reject(codeInvalidRequest)
	}
	for _, f := range req.Files {
		if f.Path == "" || len(f.Path) > maxRelPathBytes || !utf8.ValidString(f.Path) || strings.ContainsRune(f.Path, 0) {
			return req, reject(codeInvalidRequest)
		}
		if f.Mime != "" && (len(f.Mime) > maxMIMEBytes || !mimePattern.MatchString(f.Mime)) {
			return req, reject(codeInvalidRequest)
		}
		if !utf8.ValidString(f.Caption) || store.CaptionUTF16Units(f.Caption) > maxCaptionUnits || !utf8.ValidString(f.Filename) || len(f.Filename) > 4*maxFilenameBytes {
			return req, reject(codeInvalidRequest)
		}
		switch f.Presentation {
		case "", "document", "photo":
		default:
			return req, reject(codeInvalidRequest)
		}
		switch f.As {
		case "", "auto", "document":
		case "photo":
			if !photoCandidate(f) {
				return req, reject(codeUnsupportedType)
			}
		default:
			return req, reject(codeInvalidRequest)
		}
		if f.As != "" && f.Presentation != "" {
			return req, reject(codeInvalidRequest)
		}
	}
	return req, nil
}

// photoCandidate reports whether a file is named or typed as PNG/JPEG, the only
// formats the transport sends with sendPhoto. It reads agent claims only; the
// transport still checks the snapshot bytes and falls back to a document.
func photoCandidate(f artifactFile) bool {
	switch f.Mime {
	case "image/png", "image/jpeg":
		return true
	}
	switch strings.ToLower(path.Ext(sanitizeFilename(f.Filename, path.Base(f.Path)))) {
	case ".png", ".jpg", ".jpeg":
		return true
	}
	return false
}

// uploadPresentation resolves the store marker for one file. The legacy
// presentation field keeps D052 semantics (empty means document). The CP-040
// field "as" defaults to auto: a PNG/JPEG-named or -typed file is offered as a
// photo (the transport falls back to a document when the bytes do not fit) and
// everything else goes as a document with its original filename. Nothing an
// agent writes selects the host-only voice marker.
func uploadPresentation(f artifactFile) string {
	if f.Presentation != "" {
		if f.Presentation == "photo" {
			return store.UploadPresentationPhoto
		}
		return ""
	}
	switch f.As {
	case "document":
		return ""
	case "photo":
		return store.UploadPresentationPhoto
	}
	if photoCandidate(f) {
		return store.UploadPresentationPhoto
	}
	return ""
}

const maxRelPathBytes = 1100

// sanitizeFilename produces a safe display name: no path components, control or
// bidi-override characters, bounded length with the extension kept.
func sanitizeFilename(name, fallback string) string {
	for _, cand := range []string{name, fallback} {
		if s := cleanName(cand); s != "" {
			return s
		}
	}
	return "file"
}

func cleanName(name string) string {
	name = strings.ReplaceAll(name, "\\", "/")
	name = path.Base(name)
	if name == "." || name == "/" || name == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range name {
		switch {
		case r == utf8.RuneError, unicode.IsControl(r), r == '/', r == ':', r == '"',
			r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069, r == 0x200E, r == 0x200F, r == 0xFEFF:
			continue
		}
		b.WriteRune(r)
	}
	s := strings.Trim(b.String(), " .")
	if len(s) <= maxFilenameBytes {
		return s
	}
	ext := path.Ext(s)
	if len(ext) > 16 {
		ext = ""
	}
	stem := s[:maxFilenameBytes-len(ext)]
	for !utf8.ValidString(stem) {
		stem = stem[:len(stem)-1]
	}
	return stem + ext
}

// artifactParts renders the frozen plan: text parts first, then one upload per
// file in request order. The part cap is the outbox's.
func artifactParts(req artifactRequest, artifactIDs []string) ([]store.DeliveryPart, error) {
	var parts []store.DeliveryPart
	if strings.TrimSpace(req.Text) != "" {
		parts = splitText(req.Text)
	}
	for i, f := range req.Files {
		part := store.DeliveryPart{Kind: store.PartUpload, ArtifactRef: store.ArtifactRef(artifactIDs[i]), Payload: []byte(f.Caption)}
		// A hint: the transport sends a document when the file cannot be a photo.
		part.ParseMode = uploadPresentation(f)
		parts = append(parts, part)
	}
	if len(parts) == 0 || len(parts) > store.MaxDeliveryParts {
		return nil, reject(codeTooManyParts)
	}
	return parts, nil
}
