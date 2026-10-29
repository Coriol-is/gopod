package telegram

// Immutable preparation for the personal foreground reply (CP-003 PR 5).
//
// Behind the artifact flag the two host-generated attachments of a personal
// final reply, oversized code documents and the automatic voice clip, are first
// prepared as private hashed snapshots and the upload sends the verified
// snapshot bytes. The reply still goes out through the existing direct send
// helpers (reply threading, topic-gone fallback), because moving foreground
// delivery to the outbox is PR 6. With the flag off none of this runs.
//
// Both attachments are personal-only: the destination must be the configured
// owner chat. Any other chat keeps the legacy direct path unchanged, so a group
// can neither spend the personal speech credential nor consume artifact quota.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync/atomic"

	"github.com/go-telegram/bot/models"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

// ConversationSource snapshots the live registration of a chat.
type ConversationSource interface {
	SnapshotConversation(ctx context.Context, jid string, thread int, visibility store.Visibility) (store.ConversationScope, error)
}

// UploadLedger records the single direct upload of a ready clip.
type UploadLedger interface {
	ClaimGenerationUpload(ctx context.Context, id string) error
	FinishGenerationUpload(ctx context.Context, id, outcome string) error
}

// PreparedDelivery is the host wiring of immutable preparation for the
// personal chat. A nil *PreparedDelivery on the Bot means the legacy paths.
type PreparedDelivery struct {
	Scopes    ConversationSource
	Artifacts PreparationStore
	Blobs     SnapshotWriter
	// Speech may be nil: code documents are prepared, voice stays legacy.
	Speech  *SpeechPreparer
	Uploads UploadLedger
	// MaxFileBytes is the store's per-file limit.
	MaxFileBytes int64
}

// EnablePreparedDelivery turns on snapshot preparation for the personal chat.
func (b *Bot) EnablePreparedDelivery(pd *PreparedDelivery) { b.prepared = pd }

var codeDocSeq atomic.Int64

// personalConversation snapshots the owner chat's registration for thread. It
// fails for every other chat, so group traffic never reaches preparation.
func (b *Bot) personalConversation(ctx context.Context, chatID int64, thread int) (store.ConversationScope, error) {
	if b.prepared == nil || b.prepared.Scopes == nil || b.ownerChatID <= 0 || chatID != b.ownerChatID {
		return store.ConversationScope{}, errors.New("not the personal chat")
	}
	return b.prepared.Scopes.SnapshotConversation(ctx, fmt.Sprintf("tg:%d", chatID), thread, store.VisibilityConversation)
}

// codeDocBytes returns the bytes of d to upload and a release function. With
// preparation on (personal chat) the bytes are read back from a verified
// private snapshot; they are always identical to d.Body, which is what the
// legacy path uploads. A preparation failure is returned and the caller keeps
// the block inline, exactly as for a failed upload.
func (b *Bot) codeDocBytes(ctx context.Context, chatID int64, thread int, d codeDoc) ([]byte, func(), error) {
	if b.prepared == nil {
		return []byte(d.Body), func() {}, nil
	}
	conv, err := b.personalConversation(ctx, chatID, thread)
	if err != nil {
		// Not the personal chat: legacy behavior, nothing prepared.
		return []byte(d.Body), func() {}, nil
	}
	pd := b.prepared
	art := byteArtifacts{st: pd.Artifacts, blobs: pd.Blobs, maxBytes: pd.MaxFileBytes}
	body := []byte(d.Body)
	prep, err := art.open(ctx, conv.Scope, fmt.Sprintf("code:%d", codeDocSeq.Add(1)), int64(max(len(body), 1)), 0)
	if err != nil {
		return nil, nil, fmt.Errorf("prepare code document: %w", err)
	}
	release := func() { art.release(ctx, prep.ID, "code_document_done") }
	facts, err := art.fill(ctx, conv.Scope, prep, "text/plain", d.Name, body)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("prepare code document: %w", err)
	}
	data, err := art.read(prep, facts)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("prepare code document: %w", err)
	}
	return data, release, nil
}

// voiceCandidate is the personal automatic-voice selection, shared by the
// legacy and the prepared path so a migration cannot change it: only the
// owner chat, only in a voice reply mode, code blocks are never read aloud and
// an empty remainder is not spoken. It returns the text to speak.
func (b *Bot) voiceCandidate(item queue.Item, text string) (string, bool) {
	if b.ownerChatID <= 0 || item.ChatID != b.ownerChatID {
		return "", false
	}
	mode := b.getReplyMode(item.ChatID, item.IsVoice)
	if mode != "voice" && mode != "voice+text" {
		return "", false
	}
	spoken := stripCodeBlocks(text)
	if strings.TrimSpace(spoken) == "" {
		return "", false
	}
	return spoken, true
}

// preparedVoiceReply is maybeVoiceReply's prepared path. It never falls back to
// the legacy call: a failure here is reported and the reply stays text-only.
func (b *Bot) preparedVoiceReply(ctx context.Context, item queue.Item, spoken string) {
	pd := b.prepared
	conv, err := b.personalConversation(ctx, item.ChatID, item.ThreadID)
	if err != nil {
		b.log.Warn("voice not prepared: no personal scope", slog.Any("err", err))
		return
	}
	b.chatAction(ctx, item.ChatID, item.ThreadID, models.ChatActionRecordVoice)
	prepared, err := pd.Speech.Prepare(ctx, SpeechRequest{Conversation: conv, IntentKey: voiceIntentKey(item, spoken), Text: spoken})
	switch {
	case err == nil:
	case errors.Is(err, ErrSpeechNoCredential):
		return // like the legacy "no key": silently text-only
	default:
		b.log.Warn("voice not prepared", slog.String("kind", speechErrKind(err)))
		return
	}
	release := func() { pd.Speech.bytesPrep().release(ctx, prepared.PreparationID, "voice_done") }
	defer release()

	// At most one direct upload per generation, claimed before the API call.
	if err = pd.Uploads.ClaimGenerationUpload(ctx, prepared.GenerationID); err != nil {
		if errors.Is(err, store.ErrUploadClaimed) {
			b.log.Info("voice already uploaded or uncertain; not sending again", slog.String("generation", prepared.GenerationID))
		} else {
			b.log.Warn("voice upload claim failed", slog.Any("err", err))
		}
		return
	}
	audio, err := b.readClip(ctx, prepared)
	if err != nil {
		b.finishUpload(ctx, prepared.GenerationID, store.UploadRefused) // nothing reached Telegram
		b.log.Warn("voice clip unreadable; not sent", slog.Any("err", err))
		return
	}
	if err = b.sendVoiceReply(ctx, item.ChatID, item.ThreadID, item.MessageID, audio); err != nil {
		outcome := store.UploadRefused
		if classifySend(nil, err, 0).Kind == OutcomeAmbiguous {
			outcome = store.UploadUnknown
		}
		b.finishUpload(ctx, prepared.GenerationID, outcome)
		b.log.Warn("send voice reply failed", slog.String("upload", outcome))
		return
	}
	b.finishUpload(ctx, prepared.GenerationID, store.UploadSent)
}

// readClip returns the verified snapshot bytes of a prepared clip.
func (b *Bot) readClip(ctx context.Context, p PreparedSpeech) ([]byte, error) {
	a, err := b.prepared.Artifacts.GetArtifact(ctx, p.ArtifactID)
	if err != nil || a.State != store.ArtifactReady {
		return nil, ErrAudioUnavailable
	}
	return b.prepared.Blobs.ReadVerified(a.BlobKey, a.Size, a.SHA256, b.prepared.Speech.bytesPrep().maxBytes)
}

func (b *Bot) finishUpload(ctx context.Context, id, outcome string) {
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), terminalWriteTimeout)
	defer cancel()
	if err := b.prepared.Uploads.FinishGenerationUpload(wctx, id, outcome); err != nil {
		b.log.Error("record voice upload outcome", slog.String("outcome", outcome), slog.Any("err", err))
	}
}

// speechErrKind is a fixed label for logs: backend text never reaches them.
func speechErrKind(err error) string {
	switch {
	case errors.Is(err, ErrSpeechUnauthorized):
		return "unauthorized"
	case errors.Is(err, ErrSpeechRefused):
		return "refused"
	case errors.Is(err, ErrPreparationUnknown):
		return "preparation_unknown"
	case errors.Is(err, ErrSpeechInProgress):
		return "in_progress"
	case errors.Is(err, ErrGenerationFailed):
		return "already_failed"
	case errors.Is(err, ErrAudioUnavailable):
		return "audio_unavailable"
	case errors.Is(err, store.ErrArtifactQuotaExceeded), errors.Is(err, store.ErrArtifactRateLimited):
		return "storage_quota"
	}
	return "error"
}

// voiceIntentKey identifies one spoken reply durably: the turn, the inbound
// message and a digest of the text, so a retried turn presents the same key
// (and is never spoken twice) while a different reply gets its own.
func voiceIntentKey(item queue.Item, spoken string) string {
	sum := sha256.Sum256([]byte(spoken))
	return fmt.Sprintf("voice:%d:%d:%d:%d:%s", item.ChatID, item.ThreadID, item.MessageID, item.ID, hex.EncodeToString(sum[:12]))
}
