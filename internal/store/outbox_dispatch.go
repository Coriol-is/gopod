package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"regexp"
	"sync"
	"syscall"
	"time"

	picolog "github.com/Coriol-is/gopod/internal/log"
)

// MaxPartAttempts is the retry budget of one outbox part (CP-002 spec).
const MaxPartAttempts = 5

const outboxFenceKey = "outbox_fence"

var (
	// ErrOutboxLocked means another controller instance holds the outbox.
	ErrOutboxLocked = errors.New("store: another controller instance owns the outbox")
	// ErrOutboxFenced means a newer controller instance took over the outbox.
	ErrOutboxFenced = errors.New("store: outbox fence superseded by a newer controller instance")
	// ErrClaimLost means the part is no longer in the sending state of this claim.
	ErrClaimLost = errors.New("store: outbox claim is no longer in sending state")
)

// OutboxOwner is the exclusive controller-instance authority over dispatch: an
// OS file lock next to the database plus a fresh fence stored in router_state.
// Every claim and completion re-checks the fence in its own transaction, so a
// superseded instance cannot commit. It is not a multi-host lease protocol.
// No method here holds a SQL transaction across a network call: the caller
// claims (commit), performs the single API attempt, then completes (commit).
type OutboxOwner struct {
	st    *Store
	fence string
	mu    sync.Mutex
	lock  *os.File
}

// AcquireOutboxOwner takes the exclusive lock and installs a new fence.
func (s *Store) AcquireOutboxOwner(ctx context.Context) (*OutboxOwner, error) {
	f, err := os.OpenFile(s.path+".outbox.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("store: open outbox lock: %w", err)
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrOutboxLocked
		}
		return nil, fmt.Errorf("store: lock outbox: %w", err)
	}
	fence, err := newUUID()
	if err == nil {
		err = s.SetState(ctx, outboxFenceKey, fence)
	}
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("store: install outbox fence: %w", err)
	}
	return &OutboxOwner{st: s, fence: fence, lock: f}, nil
}

// Fence is the opaque token recorded with every attempt of this owner.
func (o *OutboxOwner) Fence() string { return o.fence }

// Release gives up the lock. The fence stays; the next owner replaces it.
func (o *OutboxOwner) Release() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.lock != nil {
		_ = syscall.Flock(int(o.lock.Fd()), syscall.LOCK_UN)
		_ = o.lock.Close()
		o.lock = nil
	}
}

func (o *OutboxOwner) checkFenceQ(ctx context.Context, q taskSQL) error {
	var cur string
	err := q.QueryRowContext(ctx, `SELECT value FROM router_state WHERE key=?`, outboxFenceKey).Scan(&cur)
	if err != nil || cur != o.fence {
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		return ErrOutboxFenced
	}
	return nil
}

// OutboxClaim is one part committed as sending, with its attempt row and fence.
type OutboxClaim struct {
	PlainFallback bool
	DeliveryID    string
	PartID        string
	ResultID      string
	Conversation  ConversationScope
	Ordinal       int
	Part          DeliveryPart
	Attempt       int // number of this attempt, starting at 1
	AttemptID     int64
	Fence         string
	ExpiresAt     int64
}

// maxStaleSkips bounds how many stale-scope groups one claim call cancels.
const maxStaleSkips = 64

// ClaimNext cancels groups whose scope went stale, expires due pending parts,
// and commits the next dispatchable part as sending together with an attempt
// row and the fence. A part is dispatchable only when no earlier part of its
// conversation (namespace/epoch/thread/visibility, ordered by sequence then
// ordinal) is pending, sending or uncertain, so exactly one part per
// conversation is in flight and an uncertain or not-yet-due head blocks only
// its own conversation. The registration scope is revalidated in the same
// transaction; a stale scope cancels the remaining pending parts and never
// falls back to another conversation.
func (o *OutboxOwner) ClaimNext(ctx context.Context, now time.Time) (OutboxClaim, bool, error) {
	for {
		c, found, capped, err := o.claimOnce(ctx, now)
		if err != nil || found || !capped {
			return c, found, err
		}
		// The stale-cancel cap was hit, so dispatchable parts may remain behind
		// it. Each pass cancels up to maxStaleSkips groups, so this terminates.
		if ctx.Err() != nil {
			return OutboxClaim{}, false, ctx.Err()
		}
	}
}

func (o *OutboxOwner) claimOnce(ctx context.Context, now time.Time) (out OutboxClaim, found, capped bool, err error) {
	nowMs := now.UnixMilli()
	_, err = o.st.withTx(ctx, func(q taskSQL) error {
		if err := o.checkFenceQ(ctx, q); err != nil {
			return err
		}
		if err := expireDueQ(ctx, q, nowMs); err != nil {
			return err
		}
		for i := 0; i < maxStaleSkips; i++ {
			c, ok, err := nextCandidateQ(ctx, q, nowMs)
			if err != nil || !ok {
				return err
			}
			err = validateScopeQ(ctx, q, c.Conversation.Scope)
			if errors.Is(err, ErrStaleScope) {
				if err = resolvePendingQ(ctx, q, c.DeliveryID, PartCancelled, "scope_stale", nowMs); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,attempts=attempts+1,updated_at=? WHERE part_id=? AND state=?`, PartSending, nowMs, c.PartID, PartPending); err != nil {
				return err
			}
			res, err := q.ExecContext(ctx, `INSERT INTO outbox_attempts(part_id,fence,started_at) VALUES(?,?,?)`, c.PartID, o.fence, nowMs)
			if err != nil {
				return err
			}
			if c.AttemptID, err = res.LastInsertId(); err != nil {
				return err
			}
			c.Attempt++
			c.Fence = o.fence
			if err = recomputeGroupState(ctx, q, c.DeliveryID, nowMs); err != nil {
				return err
			}
			out, found = c, true
			return nil
		}
		capped = true
		return nil
	})
	if err != nil {
		return OutboxClaim{}, false, false, fmt.Errorf("store: claim outbox part: %w", err)
	}
	return out, found, capped, nil
}

func nextCandidateQ(ctx context.Context, q taskSQL, nowMs int64) (OutboxClaim, bool, error) {
	var c OutboxClaim
	var vis, kind string
	var result sql.NullString
	var payload []byte
	sc := &c.Conversation.Scope
	err := q.QueryRowContext(ctx, `SELECT p.part_id,p.delivery_id,p.ordinal,p.kind,p.payload,p.artifact_ref,p.parse_mode,p.reply_markup,p.target_message_id,p.attempts,
 g.result_id,g.expires_at,g.namespace_id,g.namespace_epoch,g.registration_revision,g.chat_jid,g.chat_folder,g.thread_id,g.visibility
 FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id
 WHERE p.state=? AND p.next_attempt_at<=?
 AND NOT EXISTS (
  SELECT 1 FROM outbox_parts q JOIN delivery_groups h ON h.delivery_id=q.delivery_id
  WHERE h.namespace_id=g.namespace_id AND h.namespace_epoch=g.namespace_epoch AND h.thread_id=g.thread_id AND h.visibility=g.visibility
  AND q.state IN ('pending','sending','uncertain') AND h.expires_at>?
  AND (h.conversation_seq<g.conversation_seq OR (h.conversation_seq=g.conversation_seq AND q.ordinal<p.ordinal)))
 ORDER BY p.next_attempt_at,g.created_at,p.ordinal LIMIT 1`, PartPending, nowMs, nowMs).
		Scan(&c.PartID, &c.DeliveryID, &c.Ordinal, &kind, &payload, &c.Part.ArtifactRef, &c.Part.ParseMode, &c.Part.ReplyMarkup, &c.Part.TargetMessageID, &c.Attempt,
			&result, &c.ExpiresAt, &sc.NamespaceID, &sc.NamespaceEpoch, &sc.RegistrationRevision, &sc.JID, &sc.Folder, &c.Conversation.ThreadID, &vis)
	if errors.Is(err, sql.ErrNoRows) {
		return OutboxClaim{}, false, nil
	}
	if err != nil {
		return OutboxClaim{}, false, err
	}
	c.Part.Kind, c.Part.Payload = PartKind(kind), payload
	c.Conversation.Visibility, c.ResultID = Visibility(vis), result.String
	return c, true, nil
}

// expireDueQ resolves pending parts of groups whose expiry passed. A sending or
// uncertain part is never rewritten: its outcome stays unknown.
func expireDueQ(ctx context.Context, q taskSQL, nowMs int64) error {
	ids, err := queryStrings(ctx, q, `SELECT DISTINCT p.delivery_id FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE p.state=? AND g.expires_at<=?`, PartPending, nowMs)
	if err != nil {
		return err
	}
	for _, id := range ids {
		if err = resolvePendingQ(ctx, q, id, PartExpired, "expired", nowMs); err != nil {
			return err
		}
	}
	return nil
}

// resolvePendingQ moves every still-pending part of a delivery to a terminal
// state and refreshes the aggregate.
func resolvePendingQ(ctx context.Context, q taskSQL, deliveryID, state, reason string, nowMs int64) error {
	if _, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,reason_code=?,updated_at=? WHERE delivery_id=? AND state=?`, state, reason, nowMs, deliveryID, PartPending); err != nil {
		return err
	}
	return recomputeGroupState(ctx, q, deliveryID, nowMs)
}

func queryStrings(ctx context.Context, q taskSQL, query string, args ...any) ([]string, error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err = rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// complete runs fn in one transaction after verifying the fence and that the
// claim is still the open sending attempt of its part.
func (o *OutboxOwner) complete(ctx context.Context, c OutboxClaim, fn func(q taskSQL, attempts int, expiresAt, nowMs int64) error) error {
	if c.Fence != o.fence {
		return ErrOutboxFenced
	}
	_, err := o.st.withTx(ctx, func(q taskSQL) error {
		if err := o.checkFenceQ(ctx, q); err != nil {
			return err
		}
		var state string
		var attempts int
		var expires int64
		err := q.QueryRowContext(ctx, `SELECT p.state,p.attempts,g.expires_at FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE p.part_id=?`, c.PartID).Scan(&state, &attempts, &expires)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && state != PartSending) {
			return ErrClaimLost
		}
		if err != nil {
			return err
		}
		var open int
		if err = q.QueryRowContext(ctx, `SELECT count(*) FROM outbox_attempts WHERE attempt_id=? AND part_id=? AND fence=? AND ended_at IS NULL`, c.AttemptID, c.PartID, c.Fence).Scan(&open); err != nil {
			return err
		}
		if open != 1 {
			return ErrClaimLost
		}
		return fn(q, attempts, expires, time.Now().UnixMilli())
	})
	return err
}

func endAttemptQ(ctx context.Context, q taskSQL, attemptID int64, outcome, diagnostic string, nowMs int64) error {
	_, err := q.ExecContext(ctx, `UPDATE outbox_attempts SET ended_at=?,outcome=?,diagnostic=? WHERE attempt_id=?`, nowMs, outcome, sanitizeDiagnostic(diagnostic), attemptID)
	return err
}

func setPartQ(ctx context.Context, q taskSQL, c OutboxClaim, state, reason string, nextAt int64, attempts int, nowMs int64) error {
	_, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,reason_code=?,next_attempt_at=?,attempts=?,updated_at=? WHERE part_id=?`,
		state, sanitizeReason(reason), nextAt, attempts, nowMs, c.PartID)
	return err
}

// AckPart persists one remote acknowledgement atomically: the part becomes
// sent with its remote message id, the attempt closes, the group aggregate is
// recomputed and, when this ack completes a controller-owned interactive
// result, the compatibility reply memo is set by compare-and-set.
func (o *OutboxOwner) AckPart(ctx context.Context, c OutboxClaim, remoteMessageID int64) error {
	if remoteMessageID <= 0 {
		return fmt.Errorf("%w: remote message id", ErrInvalidDelivery)
	}
	err := o.complete(ctx, c, func(q taskSQL, attempts int, _, nowMs int64) error {
		if _, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,remote_message_id=?,reason_code='',updated_at=? WHERE part_id=?`, PartSent, remoteMessageID, nowMs, c.PartID); err != nil {
			return err
		}
		if err := endAttemptQ(ctx, q, c.AttemptID, "acked", "", nowMs); err != nil {
			return err
		}
		if err := recomputeGroupState(ctx, q, c.DeliveryID, nowMs); err != nil {
			return err
		}
		return setReplyMemoIfComplete(ctx, q, c)
	})
	if err != nil {
		return fmt.Errorf("store: ack outbox part: %w", err)
	}
	return nil
}

// setReplyMemoIfComplete sets turns.reply_msg_id once the whole plan of an
// interactive result is acknowledged. The memo is the last acknowledged text
// or edit message; isolated task turns never get a foreground memo.
func setReplyMemoIfComplete(ctx context.Context, q taskSQL, c OutboxClaim) error {
	if c.ResultID == "" {
		return nil
	}
	var state string
	if err := q.QueryRowContext(ctx, `SELECT state FROM delivery_groups WHERE delivery_id=?`, c.DeliveryID).Scan(&state); err != nil || state != DeliverySent {
		return err
	}
	res, err := readTurnResult(ctx, q, c.ResultID)
	if err != nil {
		return err
	}
	if res.Kind != ExecInteractive {
		return nil
	}
	var last int64
	err = q.QueryRowContext(ctx, `SELECT remote_message_id FROM outbox_parts WHERE delivery_id=? AND state=? AND kind IN ('text','edit') ORDER BY ordinal DESC LIMIT 1`, c.DeliveryID, PartSent).Scan(&last)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, m := range res.Members {
		if _, err = compareAndSetReplyMemoQ(ctx, q, m.TurnID, int(last)); err != nil && !errors.Is(err, ErrTurnNotFound) && !errors.Is(err, ErrReplyMemoUnavailable) {
			return err
		}
	}
	return nil
}

// ReleaseUnsent returns a part whose attempt is proven not accepted (an
// explicit 429 or a proven pre-dispatch failure) to pending at nextAt. The
// retry budget and the delivery expiry are enforced here: a part out of
// attempts becomes failed, and one whose next attempt would not start before
// expiry becomes expired, never silently pending. refund gives the attempt back
// (nothing reached the wire because the worker itself was cancelled). The
// resulting part state is returned.
func (o *OutboxOwner) ReleaseUnsent(ctx context.Context, c OutboxClaim, nextAt time.Time, reason, diagnostic string, refund bool) (string, error) {
	var state string
	err := o.complete(ctx, c, func(q taskSQL, attempts int, expires, nowMs int64) error {
		next := nextAt.UnixMilli()
		if refund {
			attempts--
		}
		state = PartPending
		switch {
		case !refund && attempts >= MaxPartAttempts:
			state, reason = PartFailed, "retry_budget_exhausted"
		case next >= expires:
			state, reason = PartExpired, "retry_beyond_expiry"
		}
		if state != PartPending {
			next = 0
		}
		if err := setPartQ(ctx, q, c, state, reason, max(next, 0), attempts, nowMs); err != nil {
			return err
		}
		if err := endAttemptQ(ctx, q, c.AttemptID, "not_sent", diagnostic, nowMs); err != nil {
			return err
		}
		switch state {
		case PartFailed:
			return resolvePendingQ(ctx, q, c.DeliveryID, PartCancelled, "prior_part_failed", nowMs)
		case PartExpired:
			return resolvePendingQ(ctx, q, c.DeliveryID, PartExpired, "expired", nowMs)
		}
		return recomputeGroupState(ctx, q, c.DeliveryID, nowMs)
	})
	if err != nil {
		return "", fmt.Errorf("store: release outbox part: %w", err)
	}
	return state, nil
}

// FailPart records an explicit terminal refusal (or a deterministic local
// failure before any API call). Later parts of the same delivery are cancelled:
// they must not be sent after a missing earlier part. Acknowledged parts stay.
func (o *OutboxOwner) FailPart(ctx context.Context, c OutboxClaim, reason, diagnostic string) error {
	err := o.complete(ctx, c, func(q taskSQL, attempts int, _, nowMs int64) error {
		if err := setPartQ(ctx, q, c, PartFailed, reason, 0, attempts, nowMs); err != nil {
			return err
		}
		if err := endAttemptQ(ctx, q, c.AttemptID, "refused", diagnostic, nowMs); err != nil {
			return err
		}
		return resolvePendingQ(ctx, q, c.DeliveryID, PartCancelled, "prior_part_failed", nowMs)
	})
	if err != nil {
		return fmt.Errorf("store: fail outbox part: %w", err)
	}
	return nil
}

// MarkUncertain records that the request may have been accepted. The part is
// never reclaimed for sending and blocks its own conversation.
func (o *OutboxOwner) MarkUncertain(ctx context.Context, c OutboxClaim, reason, diagnostic string) error {
	err := o.complete(ctx, c, func(q taskSQL, attempts int, _, nowMs int64) error {
		if err := setPartQ(ctx, q, c, PartUncertain, reason, 0, attempts, nowMs); err != nil {
			return err
		}
		if err := endAttemptQ(ctx, q, c.AttemptID, "uncertain", diagnostic, nowMs); err != nil {
			return err
		}
		return recomputeGroupState(ctx, q, c.DeliveryID, nowMs)
	})
	if err != nil {
		return fmt.Errorf("store: mark outbox part uncertain: %w", err)
	}
	return nil
}

// SweepSending is the restart sweep: every part still sending was left by an
// earlier process without a persisted acknowledgement, so it becomes
// uncertain and is never resent. It must run after AcquireOutboxOwner and
// before the first ClaimNext. It returns the number of parts swept.
func (o *OutboxOwner) SweepSending(ctx context.Context) (int, error) {
	return o.sweep(ctx, nil, 0, "restart_while_sending", "process restarted before acknowledgement")
}

// SweepOrphanedSending is the in-process counterpart: it marks uncertain the
// sending parts of this owner that no live worker holds any more (a terminal
// write failed). Parts in live are never touched, and neither is any part
// claimed within grace, which covers the window between the claim commit and a
// worker registering itself. Nothing is ever resent.
func (o *OutboxOwner) SweepOrphanedSending(ctx context.Context, live []string, grace time.Duration) (int, error) {
	return o.sweep(ctx, live, grace, "orphaned_while_sending", "no live worker holds this part")
}

func (o *OutboxOwner) sweep(ctx context.Context, live []string, grace time.Duration, reason, diagnostic string) (int, error) {
	skip := make(map[string]bool, len(live))
	for _, id := range live {
		skip[id] = true
	}
	n := 0
	_, err := o.st.withTx(ctx, func(q taskSQL) error {
		if err := o.checkFenceQ(ctx, q); err != nil {
			return err
		}
		nowMs := time.Now().UnixMilli()
		rows, err := q.QueryContext(ctx, `SELECT part_id,delivery_id FROM outbox_parts WHERE state=? AND updated_at<=?`, PartSending, nowMs-grace.Milliseconds())
		if err != nil {
			return err
		}
		type pd struct{ part, delivery string }
		var swept []pd
		for rows.Next() {
			var x pd
			if err = rows.Scan(&x.part, &x.delivery); err != nil {
				rows.Close()
				return err
			}
			if !skip[x.part] {
				swept = append(swept, x)
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		groups := map[string]bool{}
		for _, x := range swept {
			if _, err = q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,reason_code=?,updated_at=? WHERE part_id=?`, PartUncertain, reason, nowMs, x.part); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE outbox_attempts SET ended_at=?,outcome='uncertain',diagnostic=? WHERE part_id=? AND ended_at IS NULL`, nowMs, diagnostic, x.part); err != nil {
				return err
			}
			groups[x.delivery] = true
		}
		for id := range groups {
			if err = recomputeGroupState(ctx, q, id, nowMs); err != nil {
				return err
			}
		}
		n = len(swept)
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: sweep sending outbox parts: %w", err)
	}
	return n, nil
}

// NextDue reports the earliest future next_attempt_at of a pending part, so an
// idle dispatcher can sleep until a retry is due.
func (o *OutboxOwner) NextDue(ctx context.Context, now time.Time) (time.Time, bool, error) {
	var ms sql.NullInt64
	if err := o.st.taskDB(ctx).QueryRowContext(ctx, `SELECT min(next_attempt_at) FROM outbox_parts WHERE state=? AND next_attempt_at>?`, PartPending, now.UnixMilli()).Scan(&ms); err != nil {
		return time.Time{}, false, err
	}
	if !ms.Valid {
		return time.Time{}, false, nil
	}
	return time.UnixMilli(ms.Int64), true, nil
}

var (
	reasonPattern = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	diagPattern   = regexp.MustCompile(`^[a-z0-9_=,. :/()-]{0,200}$`)
	diagSecret    = regexp.MustCompile(`(?i)` + picolog.SecretWords)
)

// sanitizeReason keeps only a short machine code. Anything else is replaced, so
// a raw error text can never be stored as a reason.
func sanitizeReason(r string) string {
	if reasonPattern.MatchString(r) {
		return r
	}
	return "invalid_reason"
}

// sanitizeDiagnostic keeps a short lowercase diagnostic built from fixed
// fields. Free text, long values and anything resembling a secret (D012 words)
// are replaced rather than stored.
func sanitizeDiagnostic(d string) string {
	if d == "" {
		return ""
	}
	if !diagPattern.MatchString(d) || diagSecret.MatchString(d) {
		return "<redacted>"
	}
	return d
}

// PartDiagnostic is the status of one part.
type PartDiagnostic struct {
	Ordinal         int
	State           string
	Attempts        int
	RemoteMessageID int64
	ReasonCode      string
}

// DeliveryDiagnostic is the truthful per-delivery status, including partial
// acknowledgement. Accepted means queued, never delivered.
type DeliveryDiagnostic struct {
	DeliveryID string
	State      string
	Unresolved bool
	Total      int
	// Acknowledged counts sent parts; AckedPrefix is the contiguous sent run
	// from ordinal 0 that must never be resent.
	Acknowledged   int
	AckedPrefix    int
	Parts          []PartDiagnostic
	PlainFallbacks int
}

// Summary renders a one-line status without payloads, paths or raw errors.
func (d DeliveryDiagnostic) Summary() string {
	s := fmt.Sprintf("%s: %d of %d parts acknowledged", d.State, d.Acknowledged, d.Total)
	for _, p := range d.Parts {
		if p.State == PartUncertain || p.State == PartFailed || p.State == PartExpired {
			s += fmt.Sprintf("; part %d %s (%s)", p.Ordinal, p.State, p.ReasonCode)
			break
		}
	}
	if d.PlainFallbacks > 0 {
		s += fmt.Sprintf("; %d parts sent as plain text", d.PlainFallbacks)
	}
	return s
}

// DeliveryDiagnostic reports the state of one delivery group.
func (s *Store) DeliveryDiagnostic(ctx context.Context, id string) (DeliveryDiagnostic, error) {
	d, err := readDelivery(ctx, s.taskDB(ctx), id)
	if err != nil {
		return DeliveryDiagnostic{}, err
	}
	out := DeliveryDiagnostic{DeliveryID: d.ID, State: d.State, Unresolved: d.Unresolved, Total: len(d.Parts)}
	if err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT count(*) FROM outbox_plain_fallbacks f JOIN outbox_parts p ON p.part_id=f.part_id WHERE p.delivery_id=? AND p.state='sent'`, id).Scan(&out.PlainFallbacks); err != nil {
		return DeliveryDiagnostic{}, err
	}
	prefix := true
	for _, p := range d.Parts {
		out.Parts = append(out.Parts, PartDiagnostic{Ordinal: p.Ordinal, State: p.State, Attempts: p.Attempts, RemoteMessageID: p.RemoteMessageID, ReasonCode: p.ReasonCode})
		if p.State == PartSent {
			out.Acknowledged++
			if prefix {
				out.AckedPrefix++
			}
		} else {
			prefix = false
		}
	}
	return out, nil
}

// UncertainPart identifies an unresolved delivery without exposing its payload.
type UncertainPart struct {
	PartID    string
	ChatJID   string
	ThreadID  int
	ExpiresAt int64
}

// ListUncertainParts returns a bounded operator queue, including expired parts.
func (s *Store) ListUncertainParts(ctx context.Context) ([]UncertainPart, error) {
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT p.part_id,g.chat_jid,g.thread_id,g.expires_at FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE p.state='uncertain' ORDER BY p.updated_at,p.part_id LIMIT 100`)
	if err != nil {
		return nil, fmt.Errorf("store: list uncertain parts: %w", err)
	}
	defer rows.Close()
	var out []UncertainPart
	for rows.Next() {
		var p UncertainPart
		if err := rows.Scan(&p.PartID, &p.ChatJID, &p.ThreadID, &p.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SettleUncertain records operator evidence. A zero message id means proven
// unsent: cancel this part and the remainder, never automatically replay it.
// Existing attempt records remain unchanged as evidence of the uncertainty.
func (s *Store) SettleUncertain(ctx context.Context, partID string, messageID int64) error {
	if partID == "" || messageID < 0 {
		return fmt.Errorf("%w: settlement evidence", ErrInvalidDelivery)
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var c OutboxClaim
		var result sql.NullString
		var state string
		if err := q.QueryRowContext(ctx, `SELECT p.state,p.delivery_id,g.result_id FROM outbox_parts p JOIN delivery_groups g ON g.delivery_id=p.delivery_id WHERE p.part_id=?`, partID).Scan(&state, &c.DeliveryID, &result); err != nil {
			return err
		}
		if state != PartUncertain {
			return ErrClaimLost
		}
		c.PartID, c.ResultID = partID, result.String
		now := time.Now().UnixMilli()
		target, reason := PartCancelled, "operator_unsent"
		if messageID > 0 {
			target, reason = PartSent, "operator_sent"
		}
		if _, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,remote_message_id=?,reason_code=?,updated_at=? WHERE part_id=? AND state='uncertain'`, target, messageID, reason, now, partID); err != nil {
			return err
		}
		if messageID == 0 {
			return resolvePendingQ(ctx, q, c.DeliveryID, PartCancelled, "operator_unsent", now)
		}
		if err := recomputeGroupState(ctx, q, c.DeliveryID, now); err != nil {
			return err
		}
		return setReplyMemoIfComplete(ctx, q, c)
	})
	if err != nil {
		return fmt.Errorf("store: settle uncertain part: %w", err)
	}
	return nil
}
