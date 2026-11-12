package telegram

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// FinalReplyPresentation is trusted host input captured after preview ownership
// stops. Mode is already resolved (text/voice/voice+text), never looked up during
// recovery. Streaming preserves the current streaming path's text output even
// in voice-only mode; buffered voice-only without code keeps text suppressed.
type FinalReplyPresentation struct {
	Mode             string
	Streaming        bool
	ReplyToMessageID int64
	EditMessageID    int64
	ReplyMarkup      string
	RetireMessageID  int64  `json:",omitempty"`
	PreviewText      string `json:",omitempty"`
	legacy           bool
}
type finalReplyInput struct {
	Version      int
	Presentation FinalReplyPresentation
	VoiceBackend string
}

var ErrFinalReplyNoOutput = errors.New("telegram: final reply has no prepared output")

// PrepareFrozenFinalReply is the host entrypoint for an already frozen personal
// result. It does not run a model or send Telegram requests. The opt-in owner
// foreground path calls this after preview handoff; ready-intent recovery uses
// only Store.PublishReadyFinalReply. Default foreground activation remains off.
func (b *Bot) PrepareFrozenFinalReply(ctx context.Context, resultID string, presentation FinalReplyPresentation) (store.Delivery, error) {
	if b.prepared == nil || b.store == nil {
		return store.Delivery{}, store.ErrFinalReplyNotReady
	}
	if presentation.Mode != "text" && presentation.Mode != "voice" && presentation.Mode != "voice+text" {
		return store.Delivery{}, store.ErrInvalidDelivery
	}
	if presentation.RetireMessageID < 0 || presentation.RetireMessageID > 2147483647 || presentation.EditMessageID < 0 || presentation.ReplyToMessageID < 0 || presentation.EditMessageID > 2147483647 || presentation.ReplyToMessageID > 2147483647 {
		return store.Delivery{}, store.ErrInvalidDelivery
	}
	if presentation.EditMessageID != 0 && presentation.RetireMessageID != 0 {
		return store.Delivery{}, store.ErrInvalidDelivery
	}
	if _, err := parseOutboxMarkup(presentation.ReplyMarkup); err != nil {
		return store.Delivery{}, err
	}
	input := finalReplyInput{Version: 3, Presentation: presentation}
	if presentation.Mode != "text" && b.prepared.Speech != nil && b.prepared.Speech.Backend != nil {
		input.VoiceBackend = b.prepared.Speech.Backend.Name()
	}
	// Preserve the identity of already-recorded v1/v2 inputs/plans. Their bytes
	// must never be upgraded during a retry or recovery.
	if prior, e := b.store.GetFinalReply(ctx, resultID); e == nil {
		var old finalReplyInput
		if json.Unmarshal(prior.Input, &old) == nil && (old.Version == 1 || old.Version == 2) {
			input.Version = old.Version
			presentation.legacy = true
		}
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return store.Delivery{}, err
	}
	intent, err := b.store.BeginFinalReply(ctx, resultID, raw)
	if err != nil {
		return store.Delivery{}, err
	}
	if intent.State != store.FinalReplyPreparing {
		return b.store.PublishReadyFinalReply(ctx, resultID)
	}
	result, err := b.store.GetTurnResult(ctx, resultID)
	if err != nil {
		return store.Delivery{}, err
	}
	if result.Kind != store.ExecInteractive || b.ownerChatID <= 0 || result.Conversation.Scope.JID != buildChatJID(b.ownerChatID) {
		return store.Delivery{}, store.ErrScopeMismatch
	}
	text := result.Text
	if text == "" {
		text = "(empty reply)"
	}
	plan := store.FinalReplyPlan{Request: store.PublishRequest{ResultID: resultID, Conversation: result.Conversation, Owner: store.OwnerControllerResult, ExpiresAt: intent.CreatedAt + store.DefaultDeliveryExpiry.Milliseconds()}}
	wantText := presentation.Streaming || presentation.Mode != "voice" || stripCodeBlocks(text) != text
	wantVoice := presentation.Mode == "voice" || presentation.Mode == "voice+text"

	// Code preparation is deterministic from the immutable source, never from a
	// mutable model workspace. A failed snapshot keeps that exact block inline.
	var codePreparations []string
	if wantText {
		split := splitLongCode(text)
		extracted := make([]bool, len(split.docs))
		if len(split.docs) > store.MaxCodeDocuments {
			plan.Notes = append(plan.Notes, "code_alternatives_budget_inline")
		}
		for i, doc := range split.docs {
			if len(split.docs) > store.MaxCodeDocuments {
				break
			}
			prep, ref, err := b.prepareFinalCode(ctx, result, doc, i)
			if err != nil {
				if ctx.Err() != nil {
					return store.Delivery{}, ctx.Err()
				}
				plan.Notes = append(plan.Notes, fmt.Sprintf("code_%d_inline", i+1))
				continue
			}
			extracted[i] = true
			codePreparations = append(codePreparations, prep)
			plan.Preparations = append(plan.Preparations, prep)
			plan.Request.Parts = append(plan.Request.Parts, store.DeliveryPart{Kind: store.PartUpload, Payload: []byte(doc.caption()), ArtifactRef: ref})
		}
		plan.Request.CodeAlternatives = freezeCodeAlternatives(split, extracted, presentation)
		plan.Request.Parts = append(plan.Request.Parts, frozenTextParts(split.text(extracted), presentation)...)

	}
	if len(plan.Request.Parts) > store.MaxDeliveryParts || (wantVoice && len(plan.Request.Parts) == store.MaxDeliveryParts) {
		return store.Delivery{}, store.ErrInvalidDelivery
	}
	if wantVoice {
		spoken := truncateSpeech(stripCodeBlocks(text))
		if strings.TrimSpace(spoken) != "" {
			if b.prepared.Speech == nil {
				plan.Notes = append(plan.Notes, "voice_unavailable")
			} else {
				speech, err := b.prepared.Speech.Prepare(ctx, SpeechRequest{Conversation: result.Conversation, IntentKey: "final:" + resultID, Text: spoken})
				if err != nil {
					if ctx.Err() != nil {
						return store.Delivery{}, ctx.Err()
					}
					if errors.Is(err, ErrSpeechInProgress) || errors.Is(err, ErrPreparationUnknown) {
						return store.Delivery{}, err
					}
					plan.Notes = append(plan.Notes, "voice_"+speechErrKind(err))
				} else {
					plan.Preparations = append(plan.Preparations, speech.PreparationID)
					voice := store.DeliveryPart{Kind: store.PartUpload, ArtifactRef: speech.Ref, ParseMode: store.UploadPresentationVoice, ReplyToMessageID: presentation.ReplyToMessageID}
					if presentation.Streaming {
						plan.Request.Parts = append(plan.Request.Parts, voice)
					} else {
						// deliverSyncReply sends voice before documents and text.
						// Preparation still completes before any dispatch or publish.
						plan.Request.Parts = append([]store.DeliveryPart{voice}, plan.Request.Parts...)
						if a := plan.Request.CodeAlternatives; a != nil {
							a.Prefix++
							for i := range a.Documents {
								a.Documents[i]++
							}
						}
					}
				}
			}
		}
	}
	boundFinalCodePlan(&plan, text, presentation, codePreparations)
	if len(plan.Request.Parts) == 0 {
		return store.Delivery{}, ErrFinalReplyNoOutput
	}
	// Never abandon prepared bytes on an ambiguous stage/commit error: the ready
	// plan may already reference them. Unused reservations expire via existing GC.
	if _, err = b.store.StageFinalReply(ctx, resultID, plan); err != nil {
		if errors.Is(err, store.ErrFinalReplyConflict) {
			existing, e := b.store.GetFinalReply(ctx, resultID)
			if e == nil && existing.InputHash == intent.InputHash && existing.State != store.FinalReplyPreparing {
				return b.store.PublishReadyFinalReply(ctx, resultID)
			}
		}
		return store.Delivery{}, err
	}
	return b.store.PublishReadyFinalReply(ctx, resultID)
}

func (b *Bot) prepareFinalCode(ctx context.Context, result store.TurnResult, doc codeDoc, index int) (string, string, error) {
	pd := b.prepared
	limit := pd.MaxFileBytes
	if limit <= 0 {
		limit = store.DefaultMaxArtifactFileBytes
	}
	art := byteArtifacts{st: pd.Artifacts, blobs: pd.Blobs, maxBytes: limit}
	body := []byte(doc.Body)
	prep, err := art.open(ctx, result.Conversation.Scope, fmt.Sprintf("final:%s:code:%d", result.ID, index), int64(max(1, len(body))), 24*time.Hour)
	if err != nil {
		return "", "", err
	}
	facts, err := art.fill(ctx, result.Conversation.Scope, prep, "text/plain", doc.Name, body)
	if err != nil {
		art.release(ctx, prep.ID, "code_snapshot_failed")
		return "", "", err
	}
	if _, err = art.read(prep, facts); err != nil {
		art.release(ctx, prep.ID, "code_snapshot_failed")
		return "", "", err
	}
	return prep.ID, store.ArtifactRef(facts.ArtifactID), nil
}
