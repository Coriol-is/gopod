package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Turn statuses. See the state machine in
// docs/superpowers/specs/2026-10-03-durable-turns-design.md §2.
const (
	TurnPending     = "pending"
	TurnRunning     = "running"
	TurnDone        = "done"
	TurnFailed      = "failed"
	TurnInterrupted = "interrupted"
)

// ErrTurnNotFound is returned when a turn id does not exist.
var ErrTurnNotFound = errors.New("store: turn not found")

// Turn is one row of the turns table: one agent run requested by a
// Telegram message or a scheduled task. Times are unix milliseconds.
type Turn struct {
	ID               int64
	Source           string // "telegram" | "task"
	SourceID         string // tg message id | "<task_id>:<next_run>"
	ChatFolder       string
	ChatID           int64 // Telegram chat id, 0 for tasks
	TGMessageID      int64
	IsOwner          bool
	IsVoice          bool
	Text             string
	FilePath         string
	Status           string
	Attempts         int
	PlaceholderMsgID int
	ReplyMsgID       int
	Error            string
	CreatedAt        int64
	StartedAt        int64
	FinishedAt       int64
}

const turnColumns = `id, source, source_id, chat_folder, chat_id, tg_message_id,
	is_owner, is_voice, text, file_path, status, attempts,
	placeholder_msg_id, reply_msg_id, error, created_at, started_at, finished_at`

// InsertTurn inserts t as a pending turn. The UNIQUE(source, source_id)
// constraint makes this idempotent: a duplicate returns (0, true, nil)
// and writes nothing.
func (s *Store) InsertTurn(ctx context.Context, t Turn) (int64, bool, error) {
	if t.Source == "" || t.SourceID == "" {
		return 0, false, errors.New("store: InsertTurn: empty Source/SourceID")
	}
	if t.ChatFolder == "" {
		return 0, false, errors.New("store: InsertTurn: empty ChatFolder")
	}
	if t.Status == "" {
		t.Status = TurnPending
	}
	if t.CreatedAt == 0 {
		t.CreatedAt = time.Now().UnixMilli()
	}
	const stmt = `
		INSERT INTO turns
		  (source, source_id, chat_folder, chat_id, tg_message_id, is_owner, is_voice,
		   text, file_path, status, attempts, placeholder_msg_id, reply_msg_id,
		   error, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(source, source_id) DO NOTHING`
	res, err := s.db.ExecContext(ctx, stmt,
		t.Source, t.SourceID, t.ChatFolder, t.ChatID, t.TGMessageID,
		boolToInt(t.IsOwner), boolToInt(t.IsVoice),
		t.Text, nullableString(t.FilePath), t.Status, t.Attempts,
		t.PlaceholderMsgID, t.ReplyMsgID, nullableString(t.Error), t.CreatedAt)
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn rows affected: %w", err)
	}
	if n == 0 {
		return 0, true, nil
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, false, fmt.Errorf("store: InsertTurn last id: %w", err)
	}
	return id, false, nil
}

// MarkTurnRunning sets status=running, increments attempts and stamps
// started_at. Called once per attempt.
func (s *Store) MarkTurnRunning(ctx context.Context, id int64) error {
	const stmt = `
		UPDATE turns
		   SET status = ?, attempts = attempts + 1, started_at = ?
		 WHERE id = ?`
	return s.execTurn(ctx, "MarkTurnRunning", stmt, TurnRunning, time.Now().UnixMilli(), id)
}

// MarkTurnFinished sets a terminal (or interrupted) status, the error
// text, and finished_at.
func (s *Store) MarkTurnFinished(ctx context.Context, id int64, status, errText string) error {
	const stmt = `
		UPDATE turns
		   SET status = ?, error = ?, finished_at = ?
		 WHERE id = ?`
	return s.execTurn(ctx, "MarkTurnFinished", stmt, status, nullableString(errText), time.Now().UnixMilli(), id)
}

// SetTurnPlaceholder records the Telegram message id of the streaming
// placeholder so a resumed turn can edit it instead of sending a new one.
func (s *Store) SetTurnPlaceholder(ctx context.Context, id int64, msgID int) error {
	return s.execTurn(ctx, "SetTurnPlaceholder",
		`UPDATE turns SET placeholder_msg_id = ? WHERE id = ?`, msgID, id)
}

// SetTurnReply records the Telegram message id of the final reply.
// First write wins: a resumed turn with a reply id does not run again.
func (s *Store) SetTurnReply(ctx context.Context, id int64, msgID int) error {
	return s.execTurn(ctx, "SetTurnReply",
		`UPDATE turns SET reply_msg_id = ? WHERE id = ?`, msgID, id)
}

func (s *Store) execTurn(ctx context.Context, op, stmt string, args ...any) error {
	res, err := s.db.ExecContext(ctx, stmt, args...)
	if err != nil {
		return fmt.Errorf("store: %s: %w", op, err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrTurnNotFound
	}
	return nil
}

// GetTurn returns one turn by id.
func (s *Store) GetTurn(ctx context.Context, id int64) (Turn, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+turnColumns+` FROM turns WHERE id = ?`, id)
	if err != nil {
		return Turn{}, fmt.Errorf("store: GetTurn: %w", err)
	}
	defer rows.Close()
	out, err := scanTurns(rows)
	if err != nil {
		return Turn{}, err
	}
	if len(out) == 0 {
		return Turn{}, ErrTurnNotFound
	}
	return out[0], nil
}

// ListUnfinishedTurns returns pending, running and interrupted turns in
// id order. This is what boot recovery replays.
func (s *Store) ListUnfinishedTurns(ctx context.Context) ([]Turn, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+turnColumns+` FROM turns WHERE status IN (?, ?, ?) ORDER BY id`,
		TurnPending, TurnRunning, TurnInterrupted)
	if err != nil {
		return nil, fmt.Errorf("store: ListUnfinishedTurns: %w", err)
	}
	defer rows.Close()
	return scanTurns(rows)
}

func scanTurns(rows *sql.Rows) ([]Turn, error) {
	var out []Turn
	for rows.Next() {
		var t Turn
		var filePath, errText sql.NullString
		var startedAt, finishedAt sql.NullInt64
		var isOwner, isVoice int
		if err := rows.Scan(&t.ID, &t.Source, &t.SourceID, &t.ChatFolder, &t.ChatID,
			&t.TGMessageID, &isOwner, &isVoice, &t.Text, &filePath, &t.Status,
			&t.Attempts, &t.PlaceholderMsgID, &t.ReplyMsgID, &errText,
			&t.CreatedAt, &startedAt, &finishedAt); err != nil {
			return nil, fmt.Errorf("store: scan turn: %w", err)
		}
		t.IsOwner = isOwner != 0
		t.IsVoice = isVoice != 0
		t.FilePath = filePath.String
		t.Error = errText.String
		t.StartedAt = startedAt.Int64
		t.FinishedAt = finishedAt.Int64
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: iterate turns: %w", err)
	}
	return out, nil
}
