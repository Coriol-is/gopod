package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// GetSession returns the agent session id for one conversation
// (chat folder, Telegram thread, provider). thread 0 is the main chat.
func (s *Store) GetSession(ctx context.Context, folder string, thread int, provider string) (id string, ok bool, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT session_id FROM sessions WHERE chat_folder=? AND thread_id=? AND provider=?`,
		folder, thread, provider).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("getting session: %w", err)
	}
	return id, true, nil
}

// PutSession stores or replaces the session id for one conversation.
func (s *Store) PutSession(ctx context.Context, folder string, thread int, provider, id string) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (chat_folder, thread_id, provider, session_id, updated_at)
		 VALUES (?, ?, ?, ?, ?)
		 ON CONFLICT(chat_folder, thread_id, provider)
		 DO UPDATE SET session_id=excluded.session_id, updated_at=excluded.updated_at`,
		folder, thread, provider, id, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("putting session: %w", err)
	}
	return nil
}

// DeleteSession removes one conversation's session; missing is not an error.
func (s *Store) DeleteSession(ctx context.Context, folder string, thread int, provider string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM sessions WHERE chat_folder=? AND thread_id=? AND provider=?`,
		folder, thread, provider)
	if err != nil {
		return fmt.Errorf("deleting session: %w", err)
	}
	return nil
}

// migrateSessions rebuilds a legacy sessions table (PRIMARY KEY
// chat_folder) into the (chat_folder, thread_id, provider) shape. Legacy
// rows become thread 0 / provider 'claude'. It is a no-op when the table
// already has thread_id, so it is safe on fresh and migrated DBs.
func (s *Store) migrateSessions(ctx context.Context) error {
	var n int
	if err := s.db.QueryRowContext(ctx,
		`SELECT count(*) FROM pragma_table_info('sessions') WHERE name='thread_id'`).Scan(&n); err != nil {
		return fmt.Errorf("inspecting sessions: %w", err)
	}
	if n > 0 {
		return nil
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("beginning sessions rebuild: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, stmt := range []string{
		`ALTER TABLE sessions RENAME TO sessions_old`,
		sessionsDDL,
		`INSERT INTO sessions (chat_folder, thread_id, provider, session_id, updated_at)
		   SELECT chat_folder, 0, 'claude', session_id, updated_at FROM sessions_old`,
		`DROP TABLE sessions_old`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("rebuilding sessions: %w\n--- sql ---\n%s", err, stmt)
		}
	}
	return tx.Commit()
}

const sessionsDDL = `CREATE TABLE sessions (
	  chat_folder TEXT NOT NULL,
	  thread_id   INTEGER NOT NULL DEFAULT 0,
	  provider    TEXT NOT NULL DEFAULT 'claude',
	  session_id  TEXT NOT NULL,
	  updated_at  INTEGER NOT NULL,
	  PRIMARY KEY (chat_folder, thread_id, provider)
	)`
