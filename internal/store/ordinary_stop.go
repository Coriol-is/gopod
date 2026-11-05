package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// OrdinaryStopBinding is captured from the admitted batch before native work.
// It is not reconstructed from the final (coalesced) display item.
type OrdinaryStopBinding struct {
	Conversation ConversationScope
	Members      []ResultMember
}

var ErrStopConflict = errors.New("store: ordinary Stop identity conflict")
var ErrStopPublished = errors.New("store: ordinary Stop lost to publication")

func stopIdentity(binding OrdinaryStopBinding) (string, []byte, error) {
	if !binding.Conversation.validShape() || len(binding.Members) == 0 {
		return "", nil, ErrStopConflict
	}
	binding.Members = sortedMembers(binding.Members)
	for i, m := range binding.Members {
		if m.TurnID <= 0 || m.Attempt <= 0 || (i > 0 && binding.Members[i-1].TurnID == m.TurnID) {
			return "", nil, ErrStopConflict
		}
	}
	raw, err := json.Marshal(binding)
	return sha256Hex(raw), raw, err
}

// RecordOrdinaryStop commits intent without claiming native exit. Publication
// and Stop use reciprocal checks under the Store transaction serialization.
func (s *Store) RecordOrdinaryStop(ctx context.Context, binding OrdinaryStopBinding) (string, error) {
	if _, nested := s.taskDB(ctx).(*sql.Tx); nested {
		return "", ErrStopConflict
	}
	id, raw, err := stopIdentity(binding)
	if err != nil {
		return "", err
	}
	_, err = s.withTx(ctx, func(q taskSQL) error {
		var existing []byte
		e := q.QueryRowContext(ctx, `SELECT binding FROM ordinary_stop_intents WHERE intent_id=?`, id).Scan(&existing)
		if e == nil {
			if string(existing) != string(raw) {
				return ErrStopConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if e = validateConversationQ(ctx, q, binding.Conversation); e != nil {
			return e
		}
		for _, m := range binding.Members {
			var folder, source, status string
			var thread, attempt, managed, published int
			e = q.QueryRowContext(ctx, `SELECT chat_folder,thread_id,source,status,attempts,
    EXISTS(SELECT 1 FROM task_attempts WHERE turn_id=turns.id),
    EXISTS(SELECT 1 FROM turn_result_members r JOIN delivery_groups d ON d.result_id=r.result_id WHERE r.turn_id=turns.id)
    FROM turns WHERE id=?`, m.TurnID).Scan(&folder, &thread, &source, &status, &attempt, &managed, &published)
			if e != nil {
				return e
			}
			if published != 0 {
				return ErrStopPublished
			}
			if folder != binding.Conversation.Scope.Folder || thread != binding.Conversation.ThreadID || source == "task" || managed != 0 || status != TurnRunning || attempt != m.Attempt {
				return ErrStopConflict
			}
			var count int
			if e = q.QueryRowContext(ctx, `SELECT count(*) FROM ordinary_stop_members WHERE turn_id=?`, m.TurnID).Scan(&count); e != nil {
				return e
			}
			if count != 0 {
				return ErrStopConflict
			}
		}
		if _, e = q.ExecContext(ctx, `INSERT INTO ordinary_stop_intents(intent_id,binding,created_at) VALUES(?,?,?)`, id, raw, time.Now().UnixMilli()); e != nil {
			return e
		}
		for _, m := range binding.Members {
			if _, e = q.ExecContext(ctx, `INSERT INTO ordinary_stop_members(intent_id,turn_id,attempt) VALUES(?,?,?)`, id, m.TurnID, m.Attempt); e != nil {
				return e
			}
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("record ordinary Stop: %w", err)
	}
	return id, nil
}

// FindOrdinaryStop reconciles an exact commit identity. Absence alone does not
// prove rollback after an inconclusive I/O failure; callers conservatively hold.
func (s *Store) FindOrdinaryStop(ctx context.Context, binding OrdinaryStopBinding) (string, bool, error) {
	id, raw, err := stopIdentity(binding)
	if err != nil {
		return "", false, err
	}
	var existing []byte
	err = s.taskDB(ctx).QueryRowContext(ctx, `SELECT binding FROM ordinary_stop_intents WHERE intent_id=?`, id).Scan(&existing)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if string(existing) != string(raw) {
		return "", false, ErrStopConflict
	}
	return id, true, nil
}

func ordinaryStopForTurnQ(ctx context.Context, q taskSQL, turn int64) (bool, error) {
	var n int
	err := q.QueryRowContext(ctx, `SELECT count(*) FROM ordinary_stop_members WHERE turn_id=?`, turn).Scan(&n)
	return n != 0, err
}

// RecordStopCompletion preserves returned text and a fixed outcome class, not
// raw backend errors. This is return evidence only, never native exit proof or
// publication authority. Frozen results remain linked by exact member identity.
func (s *Store) RecordStopCompletion(ctx context.Context, id, text, outcome string) error {
	if outcome != "returned" && outcome != "error" && outcome != "no_replay" {
		return ErrStopConflict
	}
	_, err := s.withTx(ctx, func(q taskSQL) error {
		var previousText, previousOutcome string
		e := q.QueryRowContext(ctx, `SELECT result_text,outcome FROM ordinary_stop_completions WHERE intent_id=?`, id).Scan(&previousText, &previousOutcome)
		if e == nil {
			if previousText != text || previousOutcome != outcome {
				return ErrStopConflict
			}
			return nil
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		_, e = q.ExecContext(ctx, `INSERT INTO ordinary_stop_completions(intent_id,result_text,outcome,created_at) VALUES(?,?,?,?)`, id, text, outcome, time.Now().UnixMilli())
		return e
	})
	return err
}

// StopCompletion is immutable return evidence, not native exit or delivery proof.
type StopCompletion struct {
	Text      string
	Outcome   string
	CreatedAt int64
}

func (s *Store) GetStopCompletion(ctx context.Context, id string) (StopCompletion, error) {
	var out StopCompletion
	err := s.taskDB(ctx).QueryRowContext(ctx, `SELECT result_text,outcome,created_at FROM ordinary_stop_completions WHERE intent_id=?`, id).Scan(&out.Text, &out.Outcome, &out.CreatedAt)
	return out, err
}

// HasOrdinaryStop is diagnostic evidence only; publication/start must check in
// their own transaction rather than use this value as an authorization token.
func (s *Store) HasOrdinaryStop(ctx context.Context, turn int64) (bool, error) {
	return ordinaryStopForTurnQ(ctx, s.taskDB(ctx), turn)
}
