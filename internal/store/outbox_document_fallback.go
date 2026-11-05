package store

import (
	"context"
	"fmt"
	"time"
)

// The immutable photo marker already authorizes this exact-byte document
// alternative (D052/D058). Selection records evidence without rewriting parts.
const documentFallbackSchema = `CREATE TABLE IF NOT EXISTS outbox_document_fallbacks (
 part_id TEXT PRIMARY KEY REFERENCES outbox_parts(part_id) ON DELETE CASCADE,
 refused_attempt_id INTEGER NOT NULL UNIQUE REFERENCES outbox_attempts(attempt_id) ON DELETE CASCADE,
 first_document_attempt_id INTEGER NOT NULL UNIQUE REFERENCES outbox_attempts(attempt_id) ON DELETE CASCADE,
 source_part_hash TEXT NOT NULL,
 policy TEXT NOT NULL DEFAULT 'photo_document_v1' CHECK(policy='photo_document_v1'))`

// BeginDocumentFallback is called only for the transport's allowlisted explicit
// photo refusal. It atomically closes that attempt and commits a separate
// document attempt. A lost commit acknowledgement must not start an HTTP call.
func (o *OutboxOwner) BeginDocumentFallback(ctx context.Context, c OutboxClaim) (OutboxClaim, error) {
	return o.beginDocumentFallback(ctx, c, time.Now().UnixMilli())
}
func (o *OutboxOwner) beginDocumentFallback(ctx context.Context, c OutboxClaim, at int64) (OutboxClaim, error) {
	next := c
	err := o.complete(ctx, c, func(q taskSQL, attempts int, expires, now int64) error {
		if c.DocumentFallback || c.Part.Kind != PartUpload || c.Part.ParseMode != UploadPresentationPhoto || attempts >= MaxPartAttempts || max(at, now) >= expires {
			return ErrInvalidDelivery
		}
		// Read the canonical stored plan rather than trusting caller-supplied scope,
		// payload or destination in a claim whose attempt identity is otherwise valid.
		delivery, err := readDelivery(ctx, q, c.DeliveryID)
		if err != nil {
			return err
		}
		if delivery.Conversation != c.Conversation || delivery.ResultID != c.ResultID || c.Ordinal < 0 || c.Ordinal >= len(delivery.Parts) {
			return ErrInvalidDelivery
		}
		part := delivery.Parts[c.Ordinal]
		hash := partHash(c.Ordinal, part.Part)
		if part.ID != c.PartID || part.Ordinal != c.Ordinal || hash != partHash(c.Ordinal, c.Part) {
			return ErrInvalidDelivery
		}
		if err = validateConversationQ(ctx, q, delivery.Conversation); err != nil {
			return err
		}
		if err = endAttemptQ(ctx, q, c.AttemptID, "refused", "class=refused code=photo_refused", now); err != nil {
			return err
		}
		res, err := q.ExecContext(ctx, `INSERT INTO outbox_attempts(part_id,fence,started_at) VALUES(?,?,?)`, c.PartID, o.fence, now)
		if err != nil {
			return err
		}
		next.AttemptID, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO outbox_document_fallbacks(part_id,refused_attempt_id,first_document_attempt_id,source_part_hash) VALUES(?,?,?,?)`, c.PartID, c.AttemptID, next.AttemptID, hash); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET attempts=attempts+1,updated_at=? WHERE part_id=?`, now, c.PartID); err != nil {
			return err
		}
		next.Attempt = attempts + 1
		next.DocumentFallback = true
		next.Part.ParseMode = ""
		return nil
	})
	if err != nil {
		return OutboxClaim{}, fmt.Errorf("store: begin document fallback: %w", err)
	}
	return next, nil
}
