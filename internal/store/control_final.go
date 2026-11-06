package store

import "context"

// ControlFinalOwnership protects a control target once an immutable final edit
// owns it, even before HTTP/ACK or after an unknown outcome. Sent text targets
// also protect a reused control row. Callback eligibility is deliberately not
// consulted: bare keyboards, partial deliveries and compaction retain ownership.
// An ACK retires exactly this control revision, without inventing a new edit or
// treating the old partial text as the final payload.
func (s *Store) ControlFinalOwnership(ctx context.Context, c ControlMessage) (held bool, err error) {
	_, err = s.withTx(ctx, func(q taskSQL) error {
		var e error
		held, e = controlFinalOwnershipQ(ctx, q, c)
		return e
	})
	return
}

func controlFinalOwnershipQ(ctx context.Context, q taskSQL, c ControlMessage) (bool, error) {
	var count, ack int
	err := q.QueryRowContext(ctx, `SELECT count(*),COALESCE(MAX(p.state='sent'),0)
 FROM delivery_groups g JOIN outbox_parts p ON p.delivery_id=g.delivery_id
 WHERE g.namespace_id=? AND g.namespace_epoch=? AND g.registration_revision=?
 AND g.chat_jid=? AND g.chat_folder=? AND g.thread_id=? AND g.visibility='conversation'
 AND g.delivery_owner='controller_result' AND g.result_id IS NOT NULL
 AND ((p.kind='edit' AND p.target_message_id=?) OR (p.kind='text' AND p.state='sent' AND p.remote_message_id=?))`,
		c.Scope.NamespaceID, c.Scope.NamespaceEpoch, c.Scope.RegistrationRevision, c.Scope.JID, c.Scope.Folder, c.ThreadID, c.MessageID, c.MessageID).Scan(&count, &ack)
	if err != nil {
		return false, err
	}
	held := count > 0
	if ack == 0 {
		return held, nil
	}
	_, err = q.ExecContext(ctx, `UPDATE telegram_control_messages SET terminal=1,revision=revision+1,acknowledged=revision+1,retry_at=0 WHERE chat_id=? AND message_id=? AND revision=? AND token=? AND (terminal=0 OR acknowledged<revision)`, c.ChatID, c.MessageID, c.Revision, c.Token)
	return held, err
}
