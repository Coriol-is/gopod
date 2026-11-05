package telegram

import (
	"encoding/json"
	"slices"

	"github.com/Coriol-is/gopod/internal/store"
)

func frozenTextParts(text string, p FinalReplyPresentation) []store.DeliveryPart {
	chunks := chunkHTML(finalReplyHTML(text), 4000)
	parts := make([]store.DeliveryPart, 0, len(chunks))
	for i, chunk := range chunks {
		part := store.DeliveryPart{Kind: store.PartText, Payload: []byte(chunk), ParseMode: "HTML"}
		if i == 0 {
			part.ReplyToMessageID = p.ReplyToMessageID
		}
		if p.EditMessageID != 0 && len(chunks) == 1 {
			part.Kind = store.PartEdit
			part.TargetMessageID = p.EditMessageID
			part.ReplyToMessageID = 0
		}
		if i == len(chunks)-1 {
			part.ReplyMarkup = p.ReplyMarkup
		}
		parts = append(parts, part)
	}
	return parts
}

func freezeCodeAlternatives(split longCodeReply, extracted []bool, p FinalReplyPresentation) *store.CodeAlternatives {
	var docs []int
	for i, ok := range extracted {
		if ok {
			docs = append(docs, i)
		}
	}
	if len(docs) == 0 {
		return nil
	}
	a := &store.CodeAlternatives{Version: 1, Prefix: len(docs)}
	for i := range docs {
		a.Documents = append(a.Documents, i)
	}
	for mask := 0; mask < 1<<len(docs); mask++ {
		sent := make([]bool, len(extracted))
		for i, d := range docs {
			sent[d] = mask&(1<<i) != 0
		}
		a.Variants = append(a.Variants, frozenTextParts(split.text(sent), p))
	}
	return a
}

// Bound the full manifest after voice preparation as well. Unused code
// reservations retain their normal TTL; they are never released on an unknown
// stage outcome. The all-inline plan contains no code artifact references.
func boundFinalCodePlan(plan *store.FinalReplyPlan, text string, p FinalReplyPresentation, codePreparations []string) {
	if plan.Request.CodeAlternatives == nil {
		return
	}
	raw, _ := json.Marshal(plan.Request)
	if len(raw) <= store.MaxCodePlanBytes {
		return
	}
	var voice *store.DeliveryPart
	for _, part := range plan.Request.Parts {
		if part.ParseMode == store.UploadPresentationVoice {
			v := part
			voice = &v
		}
	}
	plan.Request.CodeAlternatives = nil
	plan.Request.Parts = frozenTextParts(text, p)
	if voice != nil {
		if p.Streaming {
			plan.Request.Parts = append(plan.Request.Parts, *voice)
		} else {
			plan.Request.Parts = append([]store.DeliveryPart{*voice}, plan.Request.Parts...)
		}
	}
	plan.Preparations = slices.DeleteFunc(plan.Preparations, func(s string) bool { return slices.Contains(codePreparations, s) })
	plan.Notes = append(plan.Notes, "code_alternatives_budget_inline")
}
