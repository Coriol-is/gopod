package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"time"
)

const (
	FinalReplyPreparing = "preparing"
	FinalReplyReady     = "ready"
	FinalReplyPublished = "published"
)

var ErrFinalReplyConflict = errors.New("store: final reply intent or plan conflict")
var ErrFinalReplyPublishedElsewhere = errors.New("store: final reply was published by another path")
var ErrFinalReplyNotReady = errors.New("store: final reply plan is not ready")

// FinalReplyIntent holds host-selected presentation, never credentials. Input
// and Plan are immutable once recorded. A ready plan may still expire or fail
// scope/artifact validation; ready is not a delivery acknowledgement.
type FinalReplyIntent struct {
	ResultID   string
	Input      json.RawMessage
	InputHash  string
	State      string
	Plan       *FinalReplyPlan
	DeliveryID string
	CreatedAt  int64
}
type FinalReplyPlan struct {
	Request      PublishRequest
	Preparations []string
	Notes        []string // fixed preparation outcomes, never raw backend errors
}

// BeginFinalReply records the presentation before any file or paid preparation.
// It only accepts interactive personal results. Repeated identical input reuses
// the intent; a later preference change cannot rewrite the original choice.
func (s *Store) BeginFinalReply(ctx context.Context, id string, input json.RawMessage) (FinalReplyIntent, error) {
	if len(input) == 0 || len(input) > 32<<10 || !json.Valid(input) {
		return FinalReplyIntent{}, ErrInvalidDelivery
	}
	hash := sha256Hex(input)
	var out FinalReplyIntent
	_, err := s.withTx(ctx, func(q taskSQL) error {
		prior, err := readFinalReplyQ(ctx, q, id)
		if err == nil {
			if prior.InputHash != hash {
				return ErrFinalReplyConflict
			}
			out = prior
			return nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		res, err := readTurnResult(ctx, q, id)
		if err != nil {
			return err
		}
		if res.Kind != ExecInteractive {
			return ErrInvalidResult
		}
		if err = validateConversationQ(ctx, q, res.Conversation); err != nil {
			return err
		}
		var owner int
		if err = q.QueryRowContext(ctx, `SELECT is_owner FROM registered_chats WHERE jid=? AND folder=?`, res.Conversation.Scope.JID, res.Conversation.Scope.Folder).Scan(&owner); err != nil {
			return err
		}
		if owner != 1 {
			return ErrInvalidResult
		}
		now := time.Now().UnixMilli()
		_, err = q.ExecContext(ctx, `INSERT INTO final_reply_intents(result_id,input_json,input_hash,state,created_at) VALUES(?,?,?,'preparing',?)`, id, []byte(input), hash, now)
		if err != nil {
			return err
		}
		out = FinalReplyIntent{ResultID: id, Input: slices.Clone(input), InputHash: hash, State: FinalReplyPreparing, CreatedAt: now}
		return nil
	})
	return out, err
}
func readFinalReplyQ(ctx context.Context, q taskSQL, id string) (FinalReplyIntent, error) {
	var out FinalReplyIntent
	var input, plan []byte
	var hash string
	err := q.QueryRowContext(ctx, `SELECT result_id,input_json,input_hash,state,plan_json,plan_hash,delivery_id,created_at FROM final_reply_intents WHERE result_id=?`, id).Scan(&out.ResultID, &input, &out.InputHash, &out.State, &plan, &hash, &out.DeliveryID, &out.CreatedAt)
	if err != nil {
		return out, err
	}
	out.Input = input
	if sha256Hex(input) != out.InputHash {
		return out, ErrFinalReplyConflict
	}
	if len(plan) > 0 {
		if sha256Hex(plan) != hash {
			return out, ErrFinalReplyConflict
		}
		out.Plan = &FinalReplyPlan{}
		if err = json.Unmarshal(plan, out.Plan); err != nil {
			return FinalReplyIntent{}, err
		}
		out.Plan.Request.voicePresentation = true
	}
	return out, nil
}
func (s *Store) GetFinalReply(ctx context.Context, id string) (FinalReplyIntent, error) {
	return readFinalReplyQ(ctx, s.taskDB(ctx), id)
}

// StageFinalReply freezes the final bytes before dispatch. No upload occurs.
// Every upload must map bijectively to ready artifacts across the given active
// reservations, all in the result's scope. The existing TTL still applies.
func (s *Store) StageFinalReply(ctx context.Context, id string, plan FinalReplyPlan) (FinalReplyIntent, error) {
	var out FinalReplyIntent
	_, err := s.withTx(ctx, func(q taskSQL) error {
		intent, err := readFinalReplyQ(ctx, q, id)
		if err != nil {
			return err
		}
		if plan.Request.ResultID != id || plan.Request.Owner != OwnerControllerResult || plan.Request.Request != nil || plan.Request.TaskRef != "" || plan.Request.RunRef != "" {
			return ErrInvalidDelivery
		}
		plan.Request.voicePresentation = true
		if plan.Request.ExpiresAt == 0 {
			plan.Request.ExpiresAt = intent.CreatedAt + DefaultDeliveryExpiry.Milliseconds()
		}
		plan.Preparations = slices.Clone(plan.Preparations)
		slices.Sort(plan.Preparations)
		raw, err := json.Marshal(plan)
		if err != nil {
			return err
		}
		if len(raw) > 3<<20 {
			return ErrInvalidDelivery
		}
		if intent.State != FinalReplyPreparing {
			existing, _ := json.Marshal(intent.Plan)
			if string(existing) != string(raw) {
				return ErrFinalReplyConflict
			}
			out = intent
			return nil
		}
		now := time.Now().UnixMilli()
		if err = validatePublish(plan.Request, now); err != nil {
			return err
		}
		res, err := readTurnResult(ctx, q, id)
		if err != nil {
			return err
		}
		if !sameConversation(res.Conversation, plan.Request.Conversation) {
			return ErrScopeMismatch
		}
		if err = validateConversationQ(ctx, q, plan.Request.Conversation); err != nil {
			return err
		}
		if err = checkFinalPreparationsQ(ctx, q, plan, now); err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `UPDATE final_reply_intents SET plan_json=?,plan_hash=?,state='ready' WHERE result_id=?`, raw, sha256Hex(raw), id); err != nil {
			return err
		}
		out, err = readFinalReplyQ(ctx, q, id)
		return err
	})
	return out, err
}

func checkFinalPreparationsQ(ctx context.Context, q taskSQL, plan FinalReplyPlan, now int64) error {
	sc := plan.Request.Conversation.Scope
	arts := map[string]Artifact{}
	seen := map[string]bool{}
	for _, prep := range plan.Preparations {
		if seen[prep] {
			return ErrInvalidArtifact
		}
		seen[prep] = true
		reservation, err := readReservation(ctx, q, prep)
		if err != nil {
			return err
		}
		if reservation.namespaceID != sc.NamespaceID || reservation.epoch != sc.NamespaceEpoch || reservation.state != "active" {
			return ErrPreparationState
		}
		if reservation.expiresAt <= now {
			return ErrPreparationExpired
		}
		members, err := listPreparationArtifacts(ctx, q, prep)
		if err != nil {
			return err
		}
		if len(members) == 0 {
			return ErrInvalidArtifact
		}
		for _, a := range members {
			if a.State != ArtifactReady || a.NamespaceID != sc.NamespaceID || a.NamespaceEpoch != sc.NamespaceEpoch {
				return ErrInvalidArtifact
			}
			arts[a.ID] = a
		}
	}
	for _, part := range plan.Request.Parts {
		if part.Kind != PartUpload {
			continue
		}
		id, ok := ParseArtifactRef(part.ArtifactRef)
		a, found := arts[id]
		if !ok || !found {
			return ErrInvalidArtifact
		}
		if part.ParseMode == UploadPresentationVoice {
			var ready int
			if err := q.QueryRowContext(ctx, `SELECT count(*) FROM generated_preparations WHERE artifact_id=? AND kind='voice' AND state='ready' AND upload_state='none'`, a.ID).Scan(&ready); err != nil {
				return err
			}
			if ready != 1 {
				return ErrInvalidArtifact
			}
		}
		delete(arts, id)
	}
	if len(arts) != 0 {
		return ErrInvalidArtifact
	}
	return nil
}

// PublishReadyFinalReply publishes the recorded plan and attaches every artifact
// reference/releases every reservation in one transaction. Recovery calls only
// this method: no input rendering, preference lookup, filesystem or paid call.
func (s *Store) PublishReadyFinalReply(ctx context.Context, id string) (Delivery, error) {
	var out Delivery
	enlisted, err := s.withTx(ctx, func(q taskSQL) error {
		intent, err := readFinalReplyQ(ctx, q, id)
		if err != nil {
			return err
		}
		if intent.State == FinalReplyPublished {
			out, err = readDelivery(ctx, q, intent.DeliveryID)
			return err
		}
		if intent.State != FinalReplyReady || intent.Plan == nil {
			return ErrFinalReplyNotReady
		}
		plan := *intent.Plan
		now := time.Now().UnixMilli()
		if err = validatePublish(plan.Request, now); err != nil {
			return err
		}
		if err = validateConversationQ(ctx, q, plan.Request.Conversation); err != nil {
			return err
		}
		if err = checkFinalPreparationsQ(ctx, q, plan, now); err != nil {
			return err
		}
		out, err = publishDeliveryQ(ctx, q, s.outboxLimits(), plan.Request, now)
		if err != nil {
			return err
		}
		if !out.Created {
			return ErrFinalReplyPublishedElsewhere
		} // another publication path must be reconciled explicitly
		for _, part := range out.Parts {
			if part.Part.Kind != PartUpload {
				continue
			}
			aid, _ := ParseArtifactRef(part.Part.ArtifactRef)
			if _, err = q.ExecContext(ctx, `INSERT INTO artifact_refs(artifact_id,ref_kind,ref_id,created_at) VALUES(?,'outbox_part',?,?)`, aid, part.ID, now); err != nil {
				return err
			}
			if _, err = q.ExecContext(ctx, `UPDATE artifacts SET last_reference_at=? WHERE artifact_id=?`, now, aid); err != nil {
				return err
			}
		}
		for _, prep := range plan.Preparations {
			if _, err = q.ExecContext(ctx, `UPDATE artifact_reservations SET state='released' WHERE preparation_id=? AND state='active'`, prep); err != nil {
				return err
			}
		}
		_, err = q.ExecContext(ctx, `UPDATE final_reply_intents SET state='published',delivery_id=? WHERE result_id=?`, out.ID, id)
		return err
	})
	if err != nil {
		return Delivery{}, fmt.Errorf("publish final reply: %w", err)
	}
	if out.Created && !enlisted {
		s.NotifyPublished(out.ID)
	}
	return out, nil
}

// ReadyFinalReplies returns a bounded page, by result ID, for boot recovery.
func (s *Store) ReadyFinalReplies(ctx context.Context, after string, limit int) ([]string, error) {
	if limit <= 0 || limit > 100 {
		limit = 100
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT result_id FROM final_reply_intents WHERE state='ready' AND result_id>? ORDER BY result_id LIMIT ?`, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}
