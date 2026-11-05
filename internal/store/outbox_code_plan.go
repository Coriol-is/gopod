package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
)

const MaxCodeDocuments = 4
const MaxCodePlanBytes = 2 << 20
const codeInlineRefused = "code_inline_refused"

// CodeAlternatives is trusted host presentation frozen before any dispatch.
// Mask bit i means document i was acknowledged. Variants contain text/edit only.
// Parts before Prefix are uploads; an optional streaming voice follows the text.
type CodeAlternatives struct {
	Version   int
	Prefix    int
	Documents []int
	Variants  [][]DeliveryPart
}

func codeCapacity(req PublishRequest) int {
	a := req.CodeAlternatives
	if a == nil {
		return len(req.Parts)
	}
	n := 0
	for _, v := range a.Variants {
		n = max(n, len(v))
	}
	return a.Prefix + n + len(req.Parts) - a.Prefix - len(a.Variants[len(a.Variants)-1])
}

func validateCodePlan(req PublishRequest) error {
	a := req.CodeAlternatives
	if a == nil {
		return nil
	}
	if !req.voicePresentation || req.Owner != OwnerControllerResult || req.ResultID == "" || req.Request != nil || a.Version != 1 || len(a.Documents) < 1 || len(a.Documents) > MaxCodeDocuments || len(a.Variants) != 1<<len(a.Documents) || a.Prefix < 1 || a.Prefix > len(req.Parts) {
		return ErrInvalidDelivery
	}
	for i, p := range req.Parts[:a.Prefix] {
		if p.Kind != PartUpload {
			return ErrInvalidDelivery
		}
		doc := slices.Contains(a.Documents, i)
		if doc && p.ParseMode != "" || !doc && p.ParseMode != UploadPresentationVoice {
			return ErrInvalidDelivery
		}
	}
	for i, d := range a.Documents {
		if d < 0 || d >= a.Prefix || i > 0 && d <= a.Documents[i-1] {
			return ErrInvalidDelivery
		}
	}
	base := a.Variants[len(a.Variants)-1]
	tail := a.Prefix + len(base)
	if len(base) == 0 || tail > len(req.Parts) || len(req.Parts)-tail > 1 {
		return ErrInvalidDelivery
	}
	for i, p := range base {
		if partHash(i, p) != partHash(i, req.Parts[a.Prefix+i]) {
			return ErrInvalidDelivery
		}
	}
	if tail < len(req.Parts) && (req.Parts[tail].Kind != PartUpload || req.Parts[tail].ParseMode != UploadPresentationVoice) {
		return ErrInvalidDelivery
	}
	for _, v := range a.Variants {
		if len(v) == 0 {
			return ErrInvalidDelivery
		}
		for _, p := range v {
			if p.Kind != PartText && p.Kind != PartEdit {
				return ErrInvalidDelivery
			}
		}
		r := req
		r.CodeAlternatives = nil
		r.Parts = append(slices.Clone(req.Parts[:a.Prefix]), v...)
		r.Parts = append(r.Parts, req.Parts[tail:]...)
		if err := validatePublish(r, 0); err != nil {
			return err
		}
	}
	raw, err := json.Marshal(req)
	if err != nil || len(raw) > MaxCodePlanBytes {
		return ErrInvalidDelivery
	}
	return nil
}

func insertOutboxPartQ(ctx context.Context, q taskSQL, id string, i int, p DeliveryPart, now int64) error {
	partID, err := newUUID()
	if err != nil {
		return err
	}
	var payload any
	if len(p.Payload) > 0 {
		payload = p.Payload
	}
	_, err = q.ExecContext(ctx, `INSERT INTO outbox_parts(part_id,delivery_id,ordinal,kind,payload,part_hash,artifact_ref,parse_mode,reply_markup,target_message_id,reply_to_message_id,state,next_attempt_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, partID, id, i, string(p.Kind), payload, partHash(i, p), p.ArtifactRef, p.ParseMode, p.ReplyMarkup, p.TargetMessageID, p.ReplyToMessageID, PartPending, now, now)
	return err
}

func publishCodePlanQ(ctx context.Context, q taskSQL, id string, req PublishRequest) error {
	raw, err := json.Marshal(req)
	if err != nil {
		return err
	}
	_, err = q.ExecContext(ctx, `INSERT INTO outbox_code_plans(delivery_id,manifest,manifest_hash,prefix_count) VALUES(?,?,?,?)`, id, raw, sha256Hex(raw), req.CodeAlternatives.Prefix)
	return err
}

// readCodePlanQ validates the full manifest and immutable materialized parts.
func readCodePlanQ(ctx context.Context, q taskSQL, id string) (PublishRequest, sql.NullInt64, string, error) {
	var req PublishRequest
	var raw []byte
	var hash, reason string
	var mask sql.NullInt64
	err := q.QueryRowContext(ctx, `SELECT manifest,manifest_hash,selected_mask,terminal_reason FROM outbox_code_plans WHERE delivery_id=?`, id).Scan(&raw, &hash, &mask, &reason)
	if err != nil {
		return req, mask, reason, err
	}
	if len(raw) == 0 || sha256Hex(raw) != hash || json.Unmarshal(raw, &req) != nil {
		return req, mask, reason, ErrInvalidDelivery
	}
	req.voicePresentation = true
	if req.CodeAlternatives == nil {
		return req, mask, reason, ErrInvalidDelivery
	}
	if err = validateCodePlan(req); err != nil {
		return req, mask, reason, err
	}
	d, err := readDelivery(ctx, q, id)
	if err != nil {
		return req, mask, reason, err
	}
	if d.PlanHash != planHash(req) || d.Conversation != req.Conversation || d.ResultID != req.ResultID || d.ExpiresAt != req.ExpiresAt {
		return req, mask, reason, ErrInvalidDelivery
	}
	a := req.CodeAlternatives
	expected := map[int]DeliveryPart{}
	for i, p := range req.Parts[:a.Prefix] {
		expected[i] = p
	}
	tail := a.Prefix + len(a.Variants[len(a.Variants)-1])
	if tail < len(req.Parts) {
		expected[codeCapacity(req)-1] = req.Parts[tail]
	}
	if mask.Valid {
		if mask.Int64 < 0 || int(mask.Int64) >= len(a.Variants) {
			return req, mask, reason, ErrInvalidDelivery
		}
		for i, p := range a.Variants[mask.Int64] {
			expected[a.Prefix+i] = p
		}
	}
	if len(d.Parts) != len(expected) {
		return req, mask, reason, ErrInvalidDelivery
	}
	for _, p := range d.Parts {
		want, ok := expected[p.Ordinal]
		if !ok || partHash(p.Ordinal, p.Part) != partHash(p.Ordinal, want) {
			return req, mask, reason, ErrInvalidDelivery
		}
		var stored string
		if err = q.QueryRowContext(ctx, `SELECT part_hash FROM outbox_parts WHERE part_id=?`, p.ID).Scan(&stored); err != nil {
			return req, mask, reason, err
		}
		if stored != partHash(p.Ordinal, want) {
			return req, mask, reason, ErrInvalidDelivery
		}
	}
	return req, mask, reason, nil
}

// advanceCodePlansQ runs under the controller fence before claiming. Appending
// one pre-rendered suffix and selecting it are one commit, never network work.
func advanceCodePlansQ(ctx context.Context, q taskSQL, now int64) error {
	ids, err := queryStrings(ctx, q, `SELECT c.delivery_id FROM outbox_code_plans c JOIN delivery_groups g USING(delivery_id) WHERE c.selected_mask IS NULL AND c.terminal_reason='' AND g.unresolved=1
 AND (g.expires_at<=? OR (NOT EXISTS (SELECT 1 FROM topic_deletions t WHERE t.namespace_id=g.namespace_id AND t.namespace_epoch=g.namespace_epoch AND t.thread_id=g.thread_id AND t.state IN ('deleting','uncertain')) AND NOT EXISTS(SELECT 1 FROM outbox_parts p WHERE p.delivery_id=c.delivery_id AND p.ordinal<c.prefix_count AND NOT(p.state='sent' OR (p.state='failed' AND p.reason_code='code_inline_refused')))))
 ORDER BY g.created_at LIMIT 100`, now)
	if err != nil {
		return err
	}
	for _, id := range ids {
		req, _, _, err := readCodePlanQ(ctx, q, id)
		if err != nil {
			return err
		}
		if req.ExpiresAt <= now {
			if err = terminateCodePlanQ(ctx, q, id, "expired", now); err != nil {
				return err
			}
			continue
		}
		if err = validateConversationQ(ctx, q, req.Conversation); err != nil {
			if errors.Is(err, ErrStaleScope) || errors.Is(err, ErrConversationRetired) {
				if err = terminateCodePlanQ(ctx, q, id, "scope_stale", now); err != nil {
					return err
				}
				continue
			}
			// A deleting/uncertain topic is held; do not stop unrelated conversations.
			if errors.Is(err, ErrTopicDeleting) {
				continue
			}
			return err
		}
		a := req.CodeAlternatives
		mask := 0
		ready := true
		for i, ord := range a.Documents {
			var state, reason string
			if err = q.QueryRowContext(ctx, `SELECT state,reason_code FROM outbox_parts WHERE delivery_id=? AND ordinal=?`, id, ord).Scan(&state, &reason); err != nil {
				return err
			}
			if state == PartSent {
				mask |= 1 << i
			} else if state != PartFailed || reason != codeInlineRefused {
				ready = false
			}
		}
		if !ready {
			continue
		}
		for i, p := range a.Variants[mask] {
			if err = insertOutboxPartQ(ctx, q, id, a.Prefix+i, p, now); err != nil {
				return err
			}
		}
		if _, err = q.ExecContext(ctx, `UPDATE outbox_code_plans SET selected_mask=? WHERE delivery_id=? AND selected_mask IS NULL AND terminal_reason=''`, mask, id); err != nil {
			return err
		}
		if err = recomputeGroupState(ctx, q, id, now); err != nil {
			return err
		}
	}
	return nil
}

func terminateCodePlanQ(ctx context.Context, q taskSQL, id, reason string, now int64) error {
	_, err := q.ExecContext(ctx, `UPDATE outbox_code_plans SET terminal_reason=? WHERE delivery_id=? AND terminal_reason=''`, reason, id)
	if err != nil {
		return err
	}
	return resolvePendingQ(ctx, q, id, PartCancelled, reason, now)
}

// RefuseCodeDocument records only a positively classified, unsent code upload.
// It returns false for ordinary uploads so their existing behavior is unchanged.
func (o *OutboxOwner) RefuseCodeDocument(ctx context.Context, c OutboxClaim, diagnostic string) (bool, error) {
	eligible := false
	err := o.complete(ctx, c, func(q taskSQL, _ int, _ int64, now int64) error {
		req, mask, reason, err := readCodePlanQ(ctx, q, c.DeliveryID)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if mask.Valid || reason != "" || !slices.Contains(req.CodeAlternatives.Documents, c.Ordinal) {
			return nil
		}
		var actualDelivery string
		var actualOrdinal int
		if err = q.QueryRowContext(ctx, `SELECT delivery_id,ordinal FROM outbox_parts WHERE part_id=?`, c.PartID).Scan(&actualDelivery, &actualOrdinal); err != nil {
			return err
		}
		if actualDelivery != c.DeliveryID || actualOrdinal != c.Ordinal {
			return ErrInvalidDelivery
		}
		if c.Conversation != req.Conversation || c.ResultID != req.ResultID || partHash(c.Ordinal, c.Part) != partHash(c.Ordinal, req.Parts[c.Ordinal]) {
			return ErrInvalidDelivery
		}
		if err = validateConversationQ(ctx, q, req.Conversation); err != nil {
			return err
		}
		if req.ExpiresAt <= now {
			return ErrInvalidDelivery
		}
		if err = endAttemptQ(ctx, q, c.AttemptID, "refused", diagnostic, now); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET state='failed',reason_code=?,updated_at=? WHERE part_id=?`, codeInlineRefused, now, c.PartID); err != nil {
			return err
		}
		eligible = true
		return recomputeGroupState(ctx, q, c.DeliveryID, now)
	})
	return eligible, err
}

// A waiting suffix is a durable unresolved operation even if all its uploads
// are terminal. Refused documents count as substituted only after selection.
func codeAggregateQ(ctx context.Context, q taskSQL, id string, counts map[string]int, total int) (map[string]int, int, error) {
	var mask sql.NullInt64
	var reason string
	err := q.QueryRowContext(ctx, `SELECT selected_mask,terminal_reason FROM outbox_code_plans WHERE delivery_id=?`, id).Scan(&mask, &reason)
	if errors.Is(err, sql.ErrNoRows) {
		return counts, total, nil
	}
	if err != nil {
		return nil, 0, err
	}
	if !mask.Valid {
		state := PartPending
		if reason != "" {
			state = PartCancelled
			if reason == "expired" {
				state = PartExpired
			}
		}
		counts[state]++
		total++
	}
	if mask.Valid {
		var n int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM outbox_parts WHERE delivery_id=? AND state='failed' AND reason_code=?`, id, codeInlineRefused).Scan(&n); err != nil {
			return nil, 0, err
		}
		counts[PartFailed] -= n
		total -= n
	}
	return counts, total, nil
}

func codePlanError(err error) error { return fmt.Errorf("store: conditional code plan: %w", err) }
