package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	applog "github.com/Coriol-is/gopod/internal/log"
	"github.com/google/uuid"
)

const ElevatedFolder = "elevated"

var ErrElevatedClosed = errors.New("store: elevated session is not active")
var ErrReservedFolder = errors.New("store: elevated is a reserved host-session folder")

const elevatedSchema = `
CREATE TABLE IF NOT EXISTS elevated_sessions (
 id TEXT PRIMARY KEY, provider TEXT NOT NULL CHECK(provider IN ('claude','codex')),
 started_at INTEGER NOT NULL, closed_at INTEGER, close_reason TEXT NOT NULL DEFAULT ''
);
CREATE UNIQUE INDEX IF NOT EXISTS elevated_one_live ON elevated_sessions((1)) WHERE closed_at IS NULL;
CREATE TABLE IF NOT EXISTS elevated_execs (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES elevated_sessions(id),
 argv TEXT NOT NULL, started_at INTEGER NOT NULL, ended_at INTEGER, exit_code INTEGER,
 truncated INTEGER NOT NULL CHECK(truncated IN (0,1))
);
CREATE TABLE IF NOT EXISTS elevated_native_contexts (
 provider TEXT PRIMARY KEY CHECK(provider IN ('claude','codex')), context_id TEXT NOT NULL,
 updated_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS elevated_transcript (
 id INTEGER PRIMARY KEY AUTOINCREMENT, session_id TEXT NOT NULL REFERENCES elevated_sessions(id),
 role TEXT NOT NULL CHECK(role IN ('user','assistant')), body TEXT NOT NULL, created_at INTEGER NOT NULL
);`

func (s *Store) applyElevatedSchema(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, elevatedSchema); err != nil {
		return fmt.Errorf("elevated schema: %w", err)
	}
	return nil
}

type ElevatedSession struct {
	ID, Provider        string
	StartedAt, ClosedAt int64
	CloseReason         string
}
type ElevatedExec struct {
	ID                int64
	SessionID, Argv   string
	StartedAt         int64
	EndedAt, ExitCode sql.NullInt64
	Truncated         bool
}

// OpenElevatedSession commits the open intent before any runtime is created.
func (s *Store) OpenElevatedSession(ctx context.Context, provider string) (ElevatedSession, error) {
	if provider != "claude" && provider != "codex" {
		return ElevatedSession{}, errors.New("elevated: unsupported provider")
	}
	v := ElevatedSession{ID: uuid.NewString(), Provider: provider, StartedAt: time.Now().UnixMilli()}
	r, err := s.db.ExecContext(ctx, `INSERT INTO elevated_sessions(id,provider,started_at) SELECT ?,?,?
 WHERE NOT EXISTS (SELECT 1 FROM registered_chats WHERE folder=?)
 AND NOT EXISTS (SELECT 1 FROM namespace_ownership WHERE folder=?)`, v.ID, v.Provider, v.StartedAt, ElevatedFolder, ElevatedFolder)
	if err != nil {
		return ElevatedSession{}, fmt.Errorf("open elevated session: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return ElevatedSession{}, err
	}
	if n != 1 {
		return ElevatedSession{}, ErrReservedFolder
	}
	return v, nil
}

func (s *Store) ActiveElevatedSession(ctx context.Context) (ElevatedSession, bool, error) {
	var v ElevatedSession
	err := s.db.QueryRowContext(ctx, `SELECT id,provider,started_at FROM elevated_sessions WHERE closed_at IS NULL`).Scan(&v.ID, &v.Provider, &v.StartedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return v, false, nil
	}
	return v, err == nil, err
}

func (s *Store) CloseElevatedSession(ctx context.Context, id, reason string) error {
	reason, _ = boundedElevatedText(reason, 256)
	_, err := s.db.ExecContext(ctx, `UPDATE elevated_sessions SET closed_at=?,close_reason=? WHERE id=? AND closed_at IS NULL`, time.Now().UnixMilli(), reason, id)
	return err
}

func boundedElevatedText(s string, limit int) (string, bool) {
	s = applog.NewRedactor().Text(s)
	if len(s) <= limit {
		return s, false
	}
	s = s[:limit]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s, true
}

// BeginElevatedExec writes a bounded, redacted intent before Docker exec.
func (s *Store) BeginElevatedExec(ctx context.Context, session string, argv []string) (int64, error) {
	if len(argv) == 0 {
		return 0, errors.New("elevated: empty argv")
	}
	redactor := applog.NewRedactor()
	safe := make([]string, len(argv))
	for i, arg := range argv {
		safe[i] = redactor.Text(arg)
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return 0, fmt.Errorf("encode elevated argv: %w", err)
	}
	text, truncated := boundedElevatedText(string(encoded), 4096)
	r, err := s.db.ExecContext(ctx, `INSERT INTO elevated_execs(session_id,argv,started_at,truncated)
 SELECT id,?,?,? FROM elevated_sessions WHERE id=? AND closed_at IS NULL`, text, time.Now().UnixMilli(), truncated, session)
	if err != nil {
		return 0, fmt.Errorf("begin elevated exec: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return 0, err
	}
	if n != 1 {
		return 0, ErrElevatedClosed
	}
	return r.LastInsertId()
}

// FinishElevatedExec settles the host call once. Exit -1 means unknown: this
// is not process-death proof. A missing end remains explicit crash evidence.
func (s *Store) FinishElevatedExec(ctx context.Context, id int64, exitCode int) error {
	r, err := s.db.ExecContext(ctx, `UPDATE elevated_execs SET ended_at=?,exit_code=? WHERE id=? AND ended_at IS NULL`, time.Now().UnixMilli(), exitCode, id)
	if err != nil {
		return fmt.Errorf("finish elevated exec: %w", err)
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("elevated: exec missing or already settled")
	}
	return nil
}

func (s *Store) ListElevatedExecs(ctx context.Context, limit int) ([]ElevatedExec, error) {
	if limit < 1 || limit > 100 {
		return nil, errors.New("elevated: log limit must be 1..100")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id,session_id,argv,started_at,ended_at,exit_code,truncated FROM elevated_execs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ElevatedExec{}
	for rows.Next() {
		var v ElevatedExec
		if err := rows.Scan(&v.ID, &v.SessionID, &v.Argv, &v.StartedAt, &v.EndedAt, &v.ExitCode, &v.Truncated); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ElevatedNativeContext is independent of ordinary legacy/durable mappings.
func (s *Store) ElevatedNativeContext(ctx context.Context, session string) (string, error) {
	var id string
	err := s.db.QueryRowContext(ctx, `SELECT COALESCE(n.context_id,'') FROM elevated_sessions s LEFT JOIN elevated_native_contexts n ON n.provider=s.provider WHERE s.id=? AND s.closed_at IS NULL`, session).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrElevatedClosed
	}
	return id, err
}

// PutElevatedNativeContext rejects late results from a closed/replaced session.
func (s *Store) PutElevatedNativeContext(ctx context.Context, session, id string) error {
	if id == "" || len(id) > 4096 {
		return errors.New("elevated: invalid context id")
	}
	r, err := s.db.ExecContext(ctx, `INSERT INTO elevated_native_contexts(provider,context_id,updated_at)
 SELECT provider,?,? FROM elevated_sessions WHERE id=? AND closed_at IS NULL
 ON CONFLICT(provider) DO UPDATE SET context_id=excluded.context_id,updated_at=excluded.updated_at`, id, time.Now().UnixMilli(), session)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrElevatedClosed
	}
	return nil
}

func (s *Store) AppendElevatedTranscript(ctx context.Context, session, role, body string) error {
	if role != "user" && role != "assistant" {
		return errors.New("elevated: invalid transcript role")
	}
	body, _ = boundedElevatedText(body, 1<<20)
	r, err := s.db.ExecContext(ctx, `INSERT INTO elevated_transcript(session_id,role,body,created_at)
 SELECT id,?,?,? FROM elevated_sessions WHERE id=? AND closed_at IS NULL`, role, body, time.Now().UnixMilli(), session)
	if err != nil {
		return err
	}
	n, err := r.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrElevatedClosed
	}
	return nil
}

func (s *Store) ElevatedExecCount(ctx context.Context, session string) (int, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM elevated_execs WHERE session_id=?`, session).Scan(&count)
	return count, err
}
