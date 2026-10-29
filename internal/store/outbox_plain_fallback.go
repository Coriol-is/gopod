package store

import (
	"context"
	"fmt"
	"time"
)

const plainFallbackSchema = `CREATE TABLE IF NOT EXISTS outbox_plain_fallbacks (
 part_id TEXT PRIMARY KEY REFERENCES outbox_parts(part_id) ON DELETE CASCADE,
 attempt_id INTEGER NOT NULL UNIQUE REFERENCES outbox_attempts(attempt_id) ON DELETE CASCADE)`

// BeginPlainFallback closes the explicitly refused HTML attempt and commits
// exactly one new attempt before any plain-text API call. The immutable part
// and plan hash remain unchanged. A crash at any later point stays uncertain.
func (o *OutboxOwner) BeginPlainFallback(ctx context.Context, c OutboxClaim) (OutboxClaim, error) {
	return o.beginPlainFallback(ctx, c, time.Now().UnixMilli())
}

func (o *OutboxOwner) beginPlainFallback(ctx context.Context, c OutboxClaim, at int64) (OutboxClaim, error) {
	next := c
	err := o.complete(ctx, c, func(q taskSQL, attempts int, expires, now int64) error {
		if c.Part.ParseMode != "HTML" || (c.Part.Kind != PartText && c.Part.Kind != PartEdit) || c.PlainFallback || attempts >= MaxPartAttempts || max(at, now) >= expires {
			return ErrInvalidDelivery
		}
		if err := validateScopeQ(ctx, q, c.Conversation.Scope); err != nil {
			return err
		}
		if err := endAttemptQ(ctx, q, c.AttemptID, "refused", "class=refused code=parse_entities", now); err != nil {
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
		if _, err = q.ExecContext(ctx, `INSERT INTO outbox_plain_fallbacks(part_id,attempt_id) VALUES(?,?)`, c.PartID, next.AttemptID); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET attempts=attempts+1,updated_at=? WHERE part_id=?`, now, c.PartID); err != nil {
			return err
		}
		next.Attempt = attempts + 1
		next.PlainFallback = true
		return nil
	})
	if err != nil {
		return OutboxClaim{}, fmt.Errorf("store: begin plain fallback: %w", err)
	}
	return next, nil
}
