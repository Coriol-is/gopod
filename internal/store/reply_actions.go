package store

import (
	"context"
	"database/sql"
	"errors"
)

// AcknowledgedReplyAction is a read model, never an execution/replay token.
// Managed distinguishes a frozen result from a legacy turn even when no action
// is eligible. Callers must not fall back to memory for ineligible managed work.
type AcknowledgedReplyAction struct {
	Managed      bool
	Eligible     bool
	Newest       bool
	Conversation ConversationScope
	MessageID    int64
	Text         string
	ReplyMarkup  string
}

// ReplyAction reads one consistent snapshot of result membership, delivery and
// ACK evidence. The caller still binds the callback's exact destination/message
// and action data before exposing text or editing Telegram. No memo is persisted.
func (s *Store) ReplyAction(ctx context.Context, turnID int64) (AcknowledgedReplyAction, error) {
	var out AcknowledgedReplyAction
	_, err := s.withTx(ctx, func(q taskSQL) error {
		return replyActionQ(ctx, q, turnID, &out)
	})
	return out, err
}

func replyActionQ(ctx context.Context, q taskSQL, turnID int64, out *AcknowledgedReplyAction) error {
	var resultID string
	var attempt int
	err := q.QueryRowContext(ctx, `SELECT result_id,attempt FROM turn_result_members WHERE turn_id=? ORDER BY attempt DESC LIMIT 1`, turnID).Scan(&resultID, &attempt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	out.Managed = true
	var current int
	if err = q.QueryRowContext(ctx, `SELECT attempts FROM turns WHERE id=?`, turnID).Scan(&current); err != nil {
		return err
	}
	if current != attempt {
		return nil
	}
	result, err := readTurnResult(ctx, q, resultID)
	if err != nil {
		return err
	}
	if result.Kind != ExecInteractive || result.Conversation.Visibility != VisibilityConversation {
		return nil
	}
	if err = validateConversationQ(ctx, q, result.Conversation); err != nil {
		return err
	}
	var stopped int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM ordinary_stop_members s JOIN turn_result_members r ON r.turn_id=s.turn_id WHERE r.result_id=?`, resultID).Scan(&stopped); err != nil {
		return err
	}
	if stopped != 0 {
		return nil
	}
	var id string
	err = q.QueryRowContext(ctx, `SELECT delivery_id FROM delivery_groups WHERE result_id=? AND delivery_owner='controller_result' AND state='sent' AND compacted_at IS NULL`, resultID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	delivery, err := readDelivery(ctx, q, id)
	if err != nil {
		return err
	}
	if !sameConversation(result.Conversation, delivery.Conversation) {
		return ErrScopeMismatch
	}
	// Only the last text/edit can own the final action row. A voice ACK is not
	// itself a row; a selected bare-keyboard branch never grants action authority.
	var partID string
	err = q.QueryRowContext(ctx, `SELECT part_id,remote_message_id,reply_markup FROM outbox_parts WHERE delivery_id=? AND state='sent' AND kind IN ('text','edit') ORDER BY ordinal DESC LIMIT 1`, id).Scan(&partID, &out.MessageID, &out.ReplyMarkup)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	var bare int
	if err = q.QueryRowContext(ctx, `SELECT count(*) FROM outbox_keyboard_fallbacks WHERE part_id=?`, partID).Scan(&bare); err != nil {
		return err
	}
	if bare != 0 || out.MessageID <= 0 || out.ReplyMarkup == "" {
		return nil
	}
	var newer int
	sc := delivery.Conversation
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM delivery_groups g JOIN turn_results r ON r.result_id=g.result_id
 WHERE g.namespace_id=? AND g.namespace_epoch=? AND g.thread_id=? AND g.visibility=? AND g.conversation_seq>?
 AND g.delivery_owner='controller_result' AND g.state='sent' AND r.execution_kind='interactive'
 AND EXISTS(SELECT 1 FROM outbox_parts p WHERE p.delivery_id=g.delivery_id AND p.kind IN ('text','edit') AND p.state='sent')`, sc.Scope.NamespaceID, sc.Scope.NamespaceEpoch, sc.ThreadID, sc.Visibility, delivery.Sequence).Scan(&newer)
	if err != nil {
		return err
	}
	out.Conversation = sc
	out.Text = result.Text
	out.Eligible = true
	out.Newest = newer == 0
	return nil
}
