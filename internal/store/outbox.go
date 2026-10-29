package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"time"
)

// DeliveryOwner is the single host-authorized final-delivery owner of an execution.
type DeliveryOwner string

const (
	// OwnerLegacyAgentIPC keeps today's agent IPC delivery; stdout is never published.
	OwnerLegacyAgentIPC DeliveryOwner = "legacy_agent_ipc"
	// OwnerControllerResult publishes a frozen result through the controller.
	OwnerControllerResult DeliveryOwner = "controller_result"
)

// PartKind is the typed immutable operation of one outbox part.
type PartKind string

const (
	PartText   PartKind = "text"
	PartEdit   PartKind = "edit"
	PartUpload PartKind = "upload"
)

// Part and group states. A group is unresolved while any part is pending,
// sending or uncertain; uncertain is never reclaimed for sending.
const (
	PartPending   = "pending"
	PartSending   = "sending"
	PartSent      = "sent"
	PartUncertain = "uncertain"
	PartFailed    = "failed"
	PartExpired   = "expired"
	PartCancelled = "cancelled"

	DeliveryPending   = "pending"
	DeliverySending   = "sending"
	DeliveryPartial   = "partial"
	DeliverySent      = "sent"
	DeliveryUncertain = "uncertain"
	DeliveryFailed    = "failed"
	DeliveryCancelled = "cancelled"
	DeliveryExpired   = "expired"
)

// Plan and retention bounds from the CP-002 spec.
const (
	MaxDeliveryParts      = 32
	MaxPartPayloadBytes   = 64 << 10 // text/edit body; Telegram text is far smaller
	MaxReplyMarkupBytes   = 8 << 10
	MaxArtifactRefBytes   = 512
	MaxUploadCaptionBytes = 4096 // an upload part's payload is its caption
	MaxParseModeBytes     = 32
	MaxRequestKeyBytes    = 255
	DefaultDeliveryExpiry = 24 * time.Hour
)

var (
	ErrInvalidDelivery  = errors.New("store: invalid delivery")
	ErrPlanConflict     = errors.New("store: result already published with a different plan")
	ErrRequestConflict  = errors.New("store: request key reused with different bytes or plan")
	ErrRequestRejected  = errors.New("store: request was durably rejected")
	ErrScopeMismatch    = errors.New("store: result and delivery scopes differ")
	ErrDeliveryNotFound = errors.New("store: delivery not found")
)

// DeliveryPart is one exact, immutable operation. Retries never re-render it.
type DeliveryPart struct {
	Kind            PartKind
	Payload         []byte // text/edit body; an upload's optional caption
	ArtifactRef     string // opaque immutable artifact reference for uploads
	ParseMode       string
	ReplyMarkup     string
	TargetMessageID int64 // required for edits
}

// RequestIdentity is the durable dedupe identity of an IPC request. The key is
// scoped by namespace and epoch; PayloadHash is the hash of the request bytes.
type RequestIdentity struct {
	Key         string
	PayloadHash string
}

// PublishRequest is the authorized, frozen plan of one delivery.
type PublishRequest struct {
	Conversation ConversationScope
	ResultID     string // empty for IPC messages; requires OwnerControllerResult
	Owner        DeliveryOwner
	ActorRef     string
	TaskRef      string
	RunRef       string
	Parts        []DeliveryPart
	ExpiresAt    int64            // unix ms; zero means DefaultDeliveryExpiry from now
	Request      *RequestIdentity // optional
}

// OutboxPart is a stored part with its dispatch state.
type OutboxPart struct {
	ID              string
	Ordinal         int
	Part            DeliveryPart // Payload is nil after terminal compaction
	State           string
	Attempts        int
	NextAttemptAt   int64
	RemoteMessageID int64
	ReasonCode      string
}

// Delivery is a delivery group with its parts.
type Delivery struct {
	ID           string
	ResultID     string
	Conversation ConversationScope
	Owner        DeliveryOwner
	ActorRef     string
	TaskRef      string
	RunRef       string
	Sequence     int64
	ExpiresAt    int64
	PlanHash     string
	State        string
	Unresolved   bool
	CreatedAt    int64
	UpdatedAt    int64
	CompactedAt  int64
	Parts        []OutboxPart
	// Created is false when an identical earlier publication was returned. Not persisted.
	Created bool
}

func validatePublish(req PublishRequest, now int64) error {
	if !req.Conversation.validShape() {
		return fmt.Errorf("%w: conversation scope", ErrInvalidDelivery)
	}
	if req.Owner != OwnerLegacyAgentIPC && req.Owner != OwnerControllerResult {
		return fmt.Errorf("%w: delivery owner", ErrInvalidDelivery)
	}
	if req.ResultID != "" && req.Owner != OwnerControllerResult {
		return fmt.Errorf("%w: only controller_result may publish a frozen result", ErrInvalidDelivery)
	}
	if n := len(req.Parts); n == 0 || n > MaxDeliveryParts {
		return fmt.Errorf("%w: %d parts (1..%d required)", ErrInvalidDelivery, n, MaxDeliveryParts)
	}
	if req.ExpiresAt != 0 && req.ExpiresAt <= now {
		return fmt.Errorf("%w: already expired", ErrInvalidDelivery)
	}
	if r := req.Request; r != nil && (r.Key == "" || len(r.Key) > MaxRequestKeyBytes || len(r.PayloadHash) != 64) {
		return fmt.Errorf("%w: request identity", ErrInvalidDelivery)
	}
	for i, p := range req.Parts {
		if len(p.Payload) > MaxPartPayloadBytes || len(p.ReplyMarkup) > MaxReplyMarkupBytes ||
			len(p.ArtifactRef) > MaxArtifactRefBytes || len(p.ParseMode) > MaxParseModeBytes {
			return fmt.Errorf("%w: part %d exceeds a size bound", ErrInvalidDelivery, i)
		}
		ok := false
		switch p.Kind {
		case PartText:
			ok = len(p.Payload) > 0 && p.ArtifactRef == "" && p.TargetMessageID == 0
		case PartEdit:
			ok = len(p.Payload) > 0 && p.ArtifactRef == "" && p.TargetMessageID > 0
		case PartUpload:
			ok = len(p.Payload) <= MaxUploadCaptionBytes && p.ArtifactRef != "" && p.TargetMessageID == 0
		}
		if !ok {
			return fmt.Errorf("%w: part %d (%q) is malformed", ErrInvalidDelivery, i, p.Kind)
		}
	}
	return nil
}

// planHash is the immutable digest of destination, owner and every part.
// Registration revision and expiry are excluded: they change without changing
// what is sent.
func planHash(req PublishRequest) string {
	sc := req.Conversation.Scope
	h := newCanonicalHasher("gopod.delivery-plan.v1").
		str(string(req.Owner)).str(sc.NamespaceID).int(sc.NamespaceEpoch).str(sc.JID).
		int(int64(req.Conversation.ThreadID)).str(string(req.Conversation.Visibility)).
		str(req.ResultID).str(req.ActorRef).str(req.TaskRef).str(req.RunRef).int(int64(len(req.Parts)))
	for i, p := range req.Parts {
		h.str(partHash(i, p))
	}
	return h.sum()
}

func partHash(ordinal int, p DeliveryPart) string {
	return newCanonicalHasher("gopod.delivery-part.v1").int(int64(ordinal)).str(string(p.Kind)).bytes(p.Payload).
		str(p.ArtifactRef).str(p.ParseMode).str(p.ReplyMarkup).int(p.TargetMessageID).sum()
}

// PublishDelivery atomically revalidates the ScopeRef, enforces request/plan
// identity and quotas, allocates the conversation sequence, and inserts the
// group, all parts and the request tombstone. A failure leaves nothing behind.
// No filesystem or network work happens here. After a successful standalone
// commit the registered notifier is called; see SetPublishNotifier for enlisted use.
func (s *Store) PublishDelivery(ctx context.Context, req PublishRequest) (Delivery, error) {
	now := time.Now().UnixMilli()
	if err := validatePublish(req, now); err != nil {
		return Delivery{}, err
	}
	if req.ExpiresAt == 0 {
		req.ExpiresAt = now + DefaultDeliveryExpiry.Milliseconds()
	}
	var out Delivery
	enlisted, err := s.withTx(ctx, func(q taskSQL) error {
		var e error
		out, e = publishDeliveryQ(ctx, q, s.outboxLimits(), req, now)
		return e
	})
	if err != nil {
		return Delivery{}, err
	}
	if out.Created && !enlisted {
		s.NotifyPublished(out.ID)
	}
	return out, nil
}

func publishDeliveryQ(ctx context.Context, q taskSQL, limits OutboxLimits, req PublishRequest, now int64) (Delivery, error) {
	conv := req.Conversation
	sc := conv.Scope
	if err := validateScopeQ(ctx, q, sc); err != nil {
		return Delivery{}, err
	}
	hash := planHash(req)

	if req.ResultID != "" {
		res, err := readTurnResult(ctx, q, req.ResultID)
		if err != nil {
			return Delivery{}, err
		}
		if !sameConversation(res.Conversation, conv) {
			return Delivery{}, ErrScopeMismatch
		}
		var id string
		err = q.QueryRowContext(ctx, `SELECT delivery_id FROM delivery_groups WHERE result_id=?`, req.ResultID).Scan(&id)
		if err == nil {
			d, e := readDelivery(ctx, q, id)
			if e != nil {
				return Delivery{}, e
			}
			if d.PlanHash != hash {
				return Delivery{}, ErrPlanConflict
			}
			return d, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return Delivery{}, err
		}
	}

	if r := req.Request; r != nil {
		var storedHash, outcome, reason string
		var deliveryID sql.NullString
		err := q.QueryRowContext(ctx, `SELECT payload_hash,outcome,reason_code,delivery_id FROM ipc_message_requests WHERE namespace_id=? AND namespace_epoch=? AND request_key=?`,
			sc.NamespaceID, sc.NamespaceEpoch, r.Key).Scan(&storedHash, &outcome, &reason, &deliveryID)
		switch {
		case err == nil:
			if storedHash != r.PayloadHash {
				return Delivery{}, ErrRequestConflict
			}
			if outcome == "rejected" {
				return Delivery{}, fmt.Errorf("%w: %s", ErrRequestRejected, reason)
			}
			d, e := readDelivery(ctx, q, deliveryID.String)
			if e != nil {
				return Delivery{}, e
			}
			if d.PlanHash != hash {
				return Delivery{}, ErrRequestConflict
			}
			return d, nil
		case !errors.Is(err, sql.ErrNoRows):
			return Delivery{}, err
		}
	}

	if err := checkOutboxQuota(ctx, q, limits, sc.NamespaceID); err != nil {
		return Delivery{}, err
	}

	id, err := newUUID()
	if err != nil {
		return Delivery{}, err
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO conversation_sequences(namespace_id,namespace_epoch,thread_id,visibility,last_seq) VALUES(?,?,?,?,1)
 ON CONFLICT(namespace_id,namespace_epoch,thread_id,visibility) DO UPDATE SET last_seq=last_seq+1`,
		sc.NamespaceID, sc.NamespaceEpoch, conv.ThreadID, string(conv.Visibility)); err != nil {
		return Delivery{}, fmt.Errorf("store: allocate sequence: %w", err)
	}
	var seq int64
	if err = q.QueryRowContext(ctx, `SELECT last_seq FROM conversation_sequences WHERE namespace_id=? AND namespace_epoch=? AND thread_id=? AND visibility=?`,
		sc.NamespaceID, sc.NamespaceEpoch, conv.ThreadID, string(conv.Visibility)).Scan(&seq); err != nil {
		return Delivery{}, fmt.Errorf("store: read sequence: %w", err)
	}
	if _, err = q.ExecContext(ctx, `INSERT INTO delivery_groups(delivery_id,result_id,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,actor_ref,task_ref,run_ref,delivery_owner,conversation_seq,expires_at,plan_hash,part_count,state,unresolved,created_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?,?)`,
		id, nullableString(req.ResultID), sc.NamespaceID, sc.NamespaceEpoch, sc.RegistrationRevision, sc.JID, sc.Folder,
		conv.ThreadID, string(conv.Visibility), req.ActorRef, req.TaskRef, req.RunRef, string(req.Owner), seq,
		req.ExpiresAt, hash, len(req.Parts), DeliveryPending, now, now); err != nil {
		return Delivery{}, fmt.Errorf("store: insert delivery: %w", err)
	}
	for i, p := range req.Parts {
		partID, e := newUUID()
		if e != nil {
			return Delivery{}, e
		}
		var payload any
		if len(p.Payload) > 0 {
			payload = p.Payload
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO outbox_parts(part_id,delivery_id,ordinal,kind,payload,part_hash,artifact_ref,parse_mode,reply_markup,target_message_id,state,next_attempt_at,updated_at)
 VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			partID, id, i, string(p.Kind), payload, partHash(i, p), p.ArtifactRef, p.ParseMode, p.ReplyMarkup, p.TargetMessageID, PartPending, now, now); err != nil {
			return Delivery{}, fmt.Errorf("store: insert part %d: %w", i, err)
		}
	}
	if r := req.Request; r != nil {
		if _, err = q.ExecContext(ctx, `INSERT INTO ipc_message_requests(namespace_id,namespace_epoch,request_key,payload_hash,delivery_id,outcome,created_at) VALUES(?,?,?,?,?,'accepted',?)`,
			sc.NamespaceID, sc.NamespaceEpoch, r.Key, r.PayloadHash, id, now); err != nil {
			return Delivery{}, fmt.Errorf("store: insert request tombstone: %w", err)
		}
	}
	d, err := readDelivery(ctx, q, id)
	d.Created = true
	return d, err
}

// RecordRequestRejection durably remembers a rejected IPC request so a retry
// of the same bytes stays rejected and different bytes under the key conflict.
// reason must be a sanitized code, never a raw error or path.
//
// Tombstones are never dropped. Beyond the per-namespace or global cap
// (OutboxLimits) a new rejection is not stored: the call commits a counter
// (see DroppedRejections) and returns recorded=false, so an attacker flooding
// bad requests cannot grow the table without bound. Such a request is simply
// rejected again if retried.
func (s *Store) RecordRequestRejection(ctx context.Context, sc ScopeRef, req RequestIdentity, reason string) (recorded bool, err error) {
	if req.Key == "" || len(req.Key) > MaxRequestKeyBytes || len(req.PayloadHash) != 64 || reason == "" {
		return false, fmt.Errorf("%w: request identity", ErrInvalidDelivery)
	}
	limits := s.outboxLimits()
	_, err = s.withTx(ctx, func(q taskSQL) error {
		if err := validateScopeQ(ctx, q, sc); err != nil {
			return err
		}
		var hash string
		err := q.QueryRowContext(ctx, `SELECT payload_hash FROM ipc_message_requests WHERE namespace_id=? AND namespace_epoch=? AND request_key=?`,
			sc.NamespaceID, sc.NamespaceEpoch, req.Key).Scan(&hash)
		if err == nil {
			if hash != req.PayloadHash {
				return ErrRequestConflict
			}
			recorded = true
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		var global, local int
		if err = q.QueryRowContext(ctx, `SELECT count(*), coalesce(sum(namespace_id=?),0) FROM ipc_message_requests WHERE outcome='rejected'`, sc.NamespaceID).Scan(&global, &local); err != nil {
			return err
		}
		if global >= limits.MaxRejectionsGlobal || local >= limits.MaxRejectionsPerNamespace {
			_, err = q.ExecContext(ctx, `INSERT INTO router_state(key,value) VALUES(?,'1') ON CONFLICT(key) DO UPDATE SET value=CAST(value AS INTEGER)+1`, droppedRejectionsKey(sc.NamespaceID))
			return err
		}
		_, err = q.ExecContext(ctx, `INSERT INTO ipc_message_requests(namespace_id,namespace_epoch,request_key,payload_hash,outcome,reason_code,created_at) VALUES(?,?,?,?,'rejected',?,?)`,
			sc.NamespaceID, sc.NamespaceEpoch, req.Key, req.PayloadHash, reason, time.Now().UnixMilli())
		recorded = err == nil
		return err
	})
	return recorded && err == nil, err
}

func droppedRejectionsKey(namespaceID string) string { return "ipc_rejections_dropped:" + namespaceID }

// DroppedRejections reports how many rejections were not stored because a cap was reached.
func (s *Store) DroppedRejections(ctx context.Context, namespaceID string) (int64, error) {
	v, err := s.GetState(ctx, droppedRejectionsKey(namespaceID))
	if err != nil || v == "" {
		return 0, err
	}
	n, err := strconv.ParseInt(v, 10, 64)
	return n, err
}

// GetDelivery returns a delivery group with its parts.
func (s *Store) GetDelivery(ctx context.Context, id string) (Delivery, error) {
	return readDelivery(ctx, s.taskDB(ctx), id)
}

func readDelivery(ctx context.Context, q taskSQL, id string) (Delivery, error) {
	var d Delivery
	var result sql.NullString
	var compacted sql.NullInt64
	var vis, owner string
	var unresolved int
	sc := &d.Conversation.Scope
	err := q.QueryRowContext(ctx, `SELECT delivery_id,result_id,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,actor_ref,task_ref,run_ref,delivery_owner,conversation_seq,expires_at,plan_hash,state,unresolved,created_at,updated_at,compacted_at FROM delivery_groups WHERE delivery_id=?`, id).
		Scan(&d.ID, &result, &sc.NamespaceID, &sc.NamespaceEpoch, &sc.RegistrationRevision, &sc.JID, &sc.Folder, &d.Conversation.ThreadID, &vis,
			&d.ActorRef, &d.TaskRef, &d.RunRef, &owner, &d.Sequence, &d.ExpiresAt, &d.PlanHash, &d.State, &unresolved, &d.CreatedAt, &d.UpdatedAt, &compacted)
	if errors.Is(err, sql.ErrNoRows) {
		return Delivery{}, ErrDeliveryNotFound
	}
	if err != nil {
		return Delivery{}, err
	}
	d.ResultID, d.Conversation.Visibility, d.Owner, d.Unresolved, d.CompactedAt = result.String, Visibility(vis), DeliveryOwner(owner), unresolved != 0, compacted.Int64
	rows, err := q.QueryContext(ctx, `SELECT part_id,ordinal,kind,payload,artifact_ref,parse_mode,reply_markup,target_message_id,state,attempts,next_attempt_at,remote_message_id,reason_code FROM outbox_parts WHERE delivery_id=? ORDER BY ordinal`, id)
	if err != nil {
		return Delivery{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var p OutboxPart
		var kind string
		if err = rows.Scan(&p.ID, &p.Ordinal, &kind, &p.Part.Payload, &p.Part.ArtifactRef, &p.Part.ParseMode, &p.Part.ReplyMarkup,
			&p.Part.TargetMessageID, &p.State, &p.Attempts, &p.NextAttemptAt, &p.RemoteMessageID, &p.ReasonCode); err != nil {
			return Delivery{}, err
		}
		p.Part.Kind = PartKind(kind)
		d.Parts = append(d.Parts, p)
	}
	return d, rows.Err()
}

// CancelDelivery cancels every still-pending part (explicit owner/operator
// action or revoked scope). Sending and uncertain parts are never rewritten:
// their outcome is unknown and stays visible. It returns the updated group.
func (s *Store) CancelDelivery(ctx context.Context, id, reason string) (Delivery, error) {
	if reason == "" {
		return Delivery{}, fmt.Errorf("%w: cancel reason", ErrInvalidDelivery)
	}
	var out Delivery
	_, err := s.withTx(ctx, func(q taskSQL) error {
		now := time.Now().UnixMilli()
		if _, err := q.ExecContext(ctx, `UPDATE outbox_parts SET state=?,reason_code=?,updated_at=? WHERE delivery_id=? AND state=?`, PartCancelled, reason, now, id, PartPending); err != nil {
			return err
		}
		if err := recomputeGroupState(ctx, q, id, now); err != nil {
			return err
		}
		var e error
		out, e = readDelivery(ctx, q, id)
		return e
	})
	if err != nil {
		return Delivery{}, err
	}
	return out, nil
}

// recomputeGroupState derives the aggregate state from the parts, including
// partial acknowledgement. Dispatcher transitions reuse it inside their own
// transaction.
func recomputeGroupState(ctx context.Context, q taskSQL, id string, now int64) error {
	counts := map[string]int{}
	rows, err := q.QueryContext(ctx, `SELECT state,count(*) FROM outbox_parts WHERE delivery_id=? GROUP BY state`, id)
	if err != nil {
		return err
	}
	total := 0
	for rows.Next() {
		var st string
		var n int
		if err = rows.Scan(&st, &n); err != nil {
			rows.Close()
			return err
		}
		counts[st] = n
		total += n
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if total == 0 {
		return ErrDeliveryNotFound
	}
	state, unresolved := aggregateState(counts, total)
	res, err := q.ExecContext(ctx, `UPDATE delivery_groups SET state=?,unresolved=?,updated_at=? WHERE delivery_id=?`, state, boolToInt(unresolved), now, id)
	if err != nil {
		return err
	}
	if n, e := res.RowsAffected(); e != nil || n != 1 {
		return errors.Join(ErrDeliveryNotFound, e)
	}
	return nil
}

func aggregateState(c map[string]int, total int) (string, bool) {
	switch {
	case c[PartUncertain] > 0:
		return DeliveryUncertain, true
	case c[PartSending] > 0:
		return DeliverySending, true
	case c[PartPending] > 0:
		if c[PartSent] > 0 {
			return DeliveryPartial, true
		}
		return DeliveryPending, true
	case c[PartSent] == total:
		return DeliverySent, false
	case c[PartSent] > 0:
		return DeliveryPartial, false
	case c[PartFailed] > 0:
		return DeliveryFailed, false
	case c[PartExpired] > 0:
		return DeliveryExpired, false
	default:
		return DeliveryCancelled, false
	}
}
