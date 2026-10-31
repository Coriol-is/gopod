package store

import (
	"context"
	"fmt"
	"time"
)

// MemoryWrite is a new memory and its optional replacement candidates. The
// original content/provenance is immutable; only unpinned extracted or agent
// rows in the same conversation may be superseded automatically.
type MemoryWrite struct {
	Folder, Kind, Source, Title, Content, Model string
	Thread                                      int
	Vector                                      []byte
	SimilarID                                   int64
	Keyword                                     string
}

func (s *Store) InsertMemory(ctx context.Context, m MemoryWrite) (int64, error) {
	var id int64
	_, err := s.withTx(ctx, func(q taskSQL) error {
		now := time.Now().UnixMilli()
		res, err := q.ExecContext(ctx, `INSERT INTO memories(chat_folder,thread_id,kind,source,title,content,embed_model,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, m.Folder, m.Thread, m.Kind, m.Source, m.Title, m.Content, m.Model, now, now)
		if err != nil {
			return err
		}
		id, err = res.LastInsertId()
		if err != nil {
			return err
		}
		if _, err = q.ExecContext(ctx, `INSERT INTO memory_vec(rowid,embedding) VALUES(?,?)`, id, m.Vector); err != nil {
			return err
		}
		// Recheck protection in the write transaction, including a pin that arrived
		// after the similarity lookup. Unknown/manual provenance stays protected.
		eligible := ` AND chat_folder=? AND thread_id=? AND id!=? AND IFNULL(status,'active')='active' AND IFNULL(pinned,0)=0 AND source IN ('extraction','agent')`
		if m.SimilarID > 0 {
			if _, err = q.ExecContext(ctx, `UPDATE memories SET status='superseded',superseded_by=?,updated_at=? WHERE id=?`+eligible, id, now, m.SimilarID, m.Folder, m.Thread, id); err != nil {
				return err
			}
		}
		if m.Keyword != "" {
			if _, err = q.ExecContext(ctx, `UPDATE memories SET status='superseded',superseded_by=?,updated_at=? WHERE id IN (SELECT rowid FROM memory_fts WHERE memory_fts MATCH ?)`+eligible, id, now, m.Keyword, m.Folder, m.Thread, id); err != nil {
				return err
			}
		}
		if m.Kind == "conversation_summary" {
			if _, err = q.ExecContext(ctx, `UPDATE memories SET status='superseded',superseded_by=?,updated_at=? WHERE kind='conversation_summary' AND chat_folder=? AND thread_id=? AND id!=? AND IFNULL(status,'active')='active' AND IFNULL(pinned,0)=0 AND source IN ('compact','extraction','agent')`, id, now, m.Folder, m.Thread, id); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("store: insert memory: %w", err)
	}
	return id, nil
}
