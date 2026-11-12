package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// ReplyRetirementHash commits the only permitted cleanup request: an empty inline
// keyboard, exact private chat/message, no text or alternate business/inline target.
func ReplyRetirementHash(chat int64, message int) string {
	raw := fmt.Sprintf(`{"chat_id":%d,"message_id":%d,"reply_markup":{"inline_keyboard":[]}}`, chat, message)
	h := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(h[:])
}

func validateReplyRetirementQ(ctx context.Context, q taskSQL, sequence int64, owner TelegramEditOwner) error {
	var raw, visibility string
	var message, turn int64
	var thread int
	if err := q.QueryRowContext(ctx, `SELECT scope_json,thread_id,visibility,message_id,turn_id FROM telegram_reply_surfaces WHERE sequence=?`, sequence).Scan(&raw, &thread, &visibility, &message, &turn); err != nil {
		return err
	}
	var scope ScopeRef
	if err := json.Unmarshal([]byte(raw), &scope); err != nil {
		return err
	}
	if scope != owner.Scope || thread != owner.SourceThread || visibility != string(VisibilityConversation) || message != int64(owner.MessageID) || message <= 0 {
		return ErrTelegramEditHeld
	}
	if c := owner.Control; c != nil && (c.TurnID != turn || !c.Terminal || c.Acknowledged < c.Revision) {
		return ErrTelegramEditHeld
	}
	conv := ConversationScope{Scope: scope, ThreadID: thread, Visibility: VisibilityConversation}
	newest, found, err := latestReplySurfaceQ(ctx, q, conv)
	if err != nil {
		return err
	}
	if !found || newest.Sequence <= sequence || newest.MessageID == message {
		return ErrTelegramEditHeld
	}
	var claimed, rebound bool
	err = q.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM telegram_reply_retirements WHERE sequence=?), EXISTS(SELECT 1 FROM telegram_reply_surfaces WHERE sequence>? AND message_id=? AND json_extract(scope_json,'$.JID')=?)`, sequence, sequence, message, scope.JID).Scan(&claimed, &rebound)
	if err != nil {
		return err
	}
	if claimed || rebound {
		return ErrTelegramEditHeld
	}
	return nil
}

// ReplyRetirementCandidates is bounded discovery, not dispatch authority. Begin
// rechecks current scope, newer ACK and physical target under the message gate.
func (s *Store) ReplyRetirementCandidates(ctx context.Context, chat, after int64) ([]ReplySurface, error) {
	conv, err := s.SnapshotConversation(ctx, fmt.Sprintf("tg:%d", chat), 0, VisibilityConversation)
	if err != nil {
		return nil, err
	}
	rows, err := s.taskDB(ctx).QueryContext(ctx, `SELECT a.sequence,a.scope_json,a.thread_id,a.message_id,a.turn_id,a.source
 FROM telegram_reply_surfaces a WHERE a.sequence>? AND a.message_id>0 AND a.namespace_id=? AND a.namespace_epoch=? AND a.registration_revision=?
 AND NOT EXISTS(SELECT 1 FROM telegram_reply_surfaces c WHERE c.sequence>a.sequence AND c.message_id=a.message_id AND json_extract(c.scope_json,'$.JID')=?)
 AND NOT EXISTS(SELECT 1 FROM telegram_reply_retirements x WHERE x.sequence=a.sequence)
 AND EXISTS(SELECT 1 FROM telegram_reply_surfaces b WHERE b.namespace_id=a.namespace_id AND b.namespace_epoch=a.namespace_epoch AND b.registration_revision=a.registration_revision AND b.thread_id=a.thread_id AND b.visibility=a.visibility AND b.message_id>0 AND b.sequence>a.sequence)
 ORDER BY a.sequence LIMIT 8`, after, conv.Scope.NamespaceID, conv.Scope.NamespaceEpoch, conv.Scope.RegistrationRevision, conv.Scope.JID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ReplySurface
	for rows.Next() {
		var r ReplySurface
		var raw string
		if err = rows.Scan(&r.Sequence, &raw, &r.Conversation.ThreadID, &r.MessageID, &r.TurnID, &r.Source); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r.Conversation.Scope); err != nil {
			return nil, err
		}
		r.Conversation.Visibility = VisibilityConversation
		out = append(out, r)
	}
	return out, rows.Err()
}
