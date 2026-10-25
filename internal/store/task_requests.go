package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

type taskSQL interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type taskTransactionKey struct{}
type taskTransaction struct {
	owner *Store
	tx    *sql.Tx
}

func (s *Store) taskDB(ctx context.Context) taskSQL {
	if t, ok := ctx.Value(taskTransactionKey{}).(taskTransaction); ok && t.owner == s {
		return t.tx
	}
	return s.db
}

var ErrTaskRequestConflict = errors.New("store: IPC request filename reused with a different payload")

// ApplyTaskRequest commits task mutations and their host-trusted response in
// one transaction. Agent-writable filesystem receipts are never replay authority.
func (s *Store) ApplyTaskRequest(ctx context.Context, folder, file, hash string, retention int, apply func(context.Context) ([]byte, error)) ([]byte, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var previousHash string
	var result []byte
	err = tx.QueryRowContext(ctx, `SELECT payload_hash,result_json FROM ipc_task_requests WHERE source_folder=? AND request_file=?`, folder, file).Scan(&previousHash, &result)
	if err == nil {
		if previousHash != hash {
			return nil, ErrTaskRequestConflict
		}
		return result, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if retention <= 0 {
		retention = 200
	}
	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM ipc_task_requests WHERE source_folder=? AND archived=0`, folder).Scan(&pending); err != nil {
		return nil, err
	}
	if pending >= retention {
		return nil, fmt.Errorf("pending IPC result backlog is full; repair results/archive access before submitting more requests")
	}
	inner := context.WithValue(ctx, taskTransactionKey{}, taskTransaction{owner: s, tx: tx})
	result, err = apply(inner)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ipc_task_requests(source_folder,request_file,payload_hash,result_json,created_at) VALUES(?,?,?,?,?)`, folder, file, hash, result, time.Now().UnixMilli()); err != nil {
		return nil, err
	}

	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return result, nil
}

// AcknowledgeTaskRequest is called only after the host archives the request.
// Unarchived operation results are never evicted and never read from agent files.
func (s *Store) AcknowledgeTaskRequest(ctx context.Context, folder, file, hash string, retention int) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `UPDATE ipc_task_requests SET archived=1 WHERE source_folder=? AND request_file=? AND payload_hash=?`, folder, file, hash); err != nil {
		return err
	}
	if retention <= 0 {
		retention = 200
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM ipc_task_requests WHERE source_folder=? AND archived=1 AND rowid NOT IN (SELECT rowid FROM ipc_task_requests WHERE source_folder=? AND archived=1 ORDER BY created_at DESC,rowid DESC LIMIT ?)`, folder, folder, retention); err != nil {
		return err
	}
	return tx.Commit()
}
