package store

import (
	"context"
	"fmt"
	"time"
)

// Explicit keyboard refusal selects a bare version of the same text/edit.
// Selection records evidence without rewriting immutable parts or plan hashes.
const keyboardFallbackSchema = `CREATE TABLE IF NOT EXISTS outbox_keyboard_fallbacks (
 part_id TEXT PRIMARY KEY REFERENCES outbox_parts(part_id) ON DELETE CASCADE,
 refused_attempt_id INTEGER NOT NULL UNIQUE REFERENCES outbox_attempts(attempt_id) ON DELETE CASCADE,
 first_bare_attempt_id INTEGER NOT NULL UNIQUE REFERENCES outbox_attempts(attempt_id) ON DELETE CASCADE,
 source_part_hash TEXT NOT NULL,
 policy TEXT NOT NULL DEFAULT 'keyboard_bare_v1' CHECK(policy='keyboard_bare_v1'))`

// BeginKeyboardFallback is called only for the transport's allowlisted explicit
// keyboard refusal. It atomically closes that attempt and commits a separate
// bare-keyboard attempt. A lost commit acknowledgement must not start an HTTP call.
func (o *OutboxOwner) BeginKeyboardFallback(ctx context.Context, c OutboxClaim) (OutboxClaim, error) {
	return o.beginKeyboardFallback(ctx, c, time.Now().UnixMilli())
}
func (o *OutboxOwner) beginKeyboardFallback(ctx context.Context, c OutboxClaim, at int64) (OutboxClaim, error) {
	next := c
	err := o.complete(ctx, c, func(q taskSQL, attempts int, expires, now int64) error {
		if c.KeyboardFallback || (c.Part.Kind != PartText && c.Part.Kind != PartEdit) || c.Part.ReplyMarkup == "" || attempts >= MaxPartAttempts || max(at, now) >= expires {
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
		if err = endAttemptQ(ctx, q, c.AttemptID, "refused", "class=refused code=keyboard_refused", now); err != nil {
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
		if _, err = q.ExecContext(ctx, `INSERT INTO outbox_keyboard_fallbacks(part_id,refused_attempt_id,first_bare_attempt_id,source_part_hash) VALUES(?,?,?,?)`, c.PartID, c.AttemptID, next.AttemptID, hash); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET attempts=attempts+1,updated_at=? WHERE part_id=?`, now, c.PartID); err != nil {
			return err
		}
		next.Attempt = attempts + 1
		next.KeyboardFallback = true
		return nil
	})
	if err != nil {
		return OutboxClaim{}, fmt.Errorf("store: begin keyboard fallback: %w", err)
	}
	return next, nil
}
