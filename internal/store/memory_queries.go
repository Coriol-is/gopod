package store

import (
	"context"
	"encoding/binary"
	"fmt"
	"math"
	"strings"
	"time"
)

type MemoryItem struct {
	ID              int64
	ChatFolder      string
	Kind            string
	Title           string
	Content         string
	Source          string
	Score           float64 // combined score from hybrid search
	CreatedAt       time.Time
	LastRetrievedAt time.Time // zero if never retrieved
	Pinned          bool
}

const allThreads = -1
const similarCandidates = 32

func (s *Store) ListMemoriesInThread(ctx context.Context, chatFolder string, thread int, limit int) ([]MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	vis, visArgs := MemoryVisibility("", thread)
	args := append([]any{chatFolder}, visArgs...)
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chat_folder, kind, IFNULL(title,''), content, IFNULL(source,''), created_at
		  FROM memories
		 WHERE chat_folder = ?
		   AND IFNULL(status,'active') = 'active'`+vis+`
		 ORDER BY created_at DESC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, fmt.Errorf("memory: ListInThread: %w", err)
	}
	defer rows.Close()
	return ScanMemoryItems(rows)
}

func (s *Store) ListMemories(ctx context.Context, chatFolder string, limit int) ([]MemoryItem, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, chat_folder, kind, IFNULL(title,''), content, IFNULL(source,''), created_at
		  FROM memories
		 WHERE chat_folder = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, chatFolder, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: List: %w", err)
	}
	defer rows.Close()
	return ScanMemoryItems(rows)
}

func (s *Store) DeleteMemory(ctx context.Context, id int64) error {
	db := s.db
	if _, err := db.ExecContext(ctx, `DELETE FROM memory_vec WHERE rowid = ?`, id); err != nil {
		return fmt.Errorf("memory: Delete vec: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id); err != nil {
		return fmt.Errorf("memory: Delete: %w", err)
	}
	return nil
}

func (s *Store) SupersedeMemory(ctx context.Context, oldID, newID int64) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE memories SET status = 'superseded', superseded_by = ?, updated_at = ?
		 WHERE id = ?`, newID, time.Now().UnixMilli(), oldID)
	return err
}

func (s *Store) TouchMemories(ctx context.Context, ids []int64) error {
	if len(ids) == 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	for _, id := range ids {
		if _, err := s.db.ExecContext(ctx, `UPDATE memories SET last_retrieved_at = ? WHERE id = ?`, now, id); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) FindSimilarMemory(ctx context.Context, chatFolder string, thread int, vec []float32) (int64, float64, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = ?
		   AND m.chat_folder = ?
		   AND m.thread_id = ?
           AND IFNULL(m.pinned,0)=0 AND m.source IN ('extraction','agent')
		   AND IFNULL(m.status, 'active') = 'active'
		 ORDER BY v.distance ASC
		 LIMIT 1`,
		MemoryVectorBlob(vec), similarCandidates, chatFolder, thread)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, 0, rows.Err() // no existing memories
	}
	var id int64
	var dist float64
	if err := rows.Scan(&id, &dist); err != nil {
		return 0, 0, err
	}
	similarity := 1.0 / (1.0 + dist)
	return id, similarity, nil
}

func (s *Store) SearchMemoryVectors(ctx context.Context, chatFolder string, thread int, queryVec []float32, k int) ([]MemoryItem, error) {
	vis, visArgs := MemoryVisibility("m", thread)
	args := append([]any{MemoryVectorBlob(queryVec), k, chatFolder, chatFolder}, visArgs...)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, IFNULL(m.last_retrieved_at,0),
		       IFNULL(m.pinned,0), v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = ?
		   AND (? = '' OR m.chat_folder = ?)
		   AND IFNULL(m.status, 'active') = 'active'`+vis+`
		 ORDER BY v.distance ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MemoryItem
	for rows.Next() {
		var it MemoryItem
		var createdMs, retrievedMs int64
		var pinned int
		var dist float64
		if err := rows.Scan(&it.ID, &it.ChatFolder, &it.Kind, &it.Title,
			&it.Content, &it.Source, &createdMs, &retrievedMs, &pinned, &dist); err != nil {
			return nil, err
		}
		it.CreatedAt = time.UnixMilli(createdMs)
		if retrievedMs > 0 {
			it.LastRetrievedAt = time.UnixMilli(retrievedMs)
		}
		it.Pinned = pinned != 0
		it.Score = 1.0 / (1.0 + dist)
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *Store) SearchMemoryText(ctx context.Context, chatFolder string, thread int, query string, k int) ([]MemoryItem, error) {
	// Sanitize query for FTS5: wrap each word in double quotes so
	// punctuation (.,?!) doesn't break FTS5 query syntax. FTS5 MATCH
	// treats raw punctuation as operators and errors on them.
	sanitized := SanitizeMemoryFTS(query)
	if sanitized == "" {
		return nil, nil
	}
	vis, visArgs := MemoryVisibility("m", thread)
	args := append([]any{sanitized, chatFolder, chatFolder}, visArgs...)
	args = append(args, k)
	rows, err := s.db.QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, rank
		  FROM memory_fts f
		  JOIN memories m ON m.id = f.rowid
		 WHERE memory_fts MATCH ?
		   AND (? = '' OR m.chat_folder = ?)
		   AND IFNULL(m.status, 'active') = 'active'`+vis+`
		 ORDER BY rank ASC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []MemoryItem
	for rows.Next() {
		var it MemoryItem
		var createdMs int64
		var rank float64
		if err := rows.Scan(&it.ID, &it.ChatFolder, &it.Kind, &it.Title,
			&it.Content, &it.Source, &createdMs, &rank); err != nil {
			return nil, err
		}
		it.CreatedAt = time.UnixMilli(createdMs)
		it.Score = -rank // FTS5 rank is negative (lower = better)
		items = append(items, it)
	}
	return items, rows.Err()
}

func (s *Store) ArchiveStaleMemories(ctx context.Context, chatFolder string, archiveAfterDays, minCount int) (int64, error) {
	if archiveAfterDays <= 0 {
		archiveAfterDays = 90
	}
	if minCount <= 0 {
		minCount = 50
	}

	// Check if we have enough memories to justify archiving.
	var count int64
	err := s.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE chat_folder = ? AND IFNULL(status,'active') = 'active'`,
		chatFolder).Scan(&count)
	if err != nil {
		return 0, err
	}
	if count < int64(minCount) {
		return 0, nil // too few memories, skip
	}

	cutoff := time.Now().Add(-time.Duration(archiveAfterDays) * 24 * time.Hour).UnixMilli()
	res, err := s.db.ExecContext(ctx, `
		UPDATE memories SET status = 'archived', updated_at = ?
		 WHERE chat_folder = ?
		   AND IFNULL(status, 'active') = 'active'
		   AND IFNULL(pinned, 0) = 0
		   AND (last_retrieved_at IS NULL OR last_retrieved_at < ?)
		   AND created_at < ?`,
		time.Now().UnixMilli(), chatFolder, cutoff, cutoff)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func MemoryVisibility(alias string, thread int) (string, []any) {
	if thread == allThreads {
		return "", nil
	}
	p := ""
	if alias != "" {
		p = alias + "."
	}
	cond := " AND (" + p + "kind != 'conversation_summary' OR " + p + "thread_id = ?)"
	args := []any{thread}
	if thread != 0 {
		cond += " AND " + p + "thread_id IN (0, ?)"
		args = append(args, thread)
	}
	return cond, args
}

func SanitizeMemoryFTS(s string) string {
	words := strings.Fields(s)
	var clean []string
	for _, w := range words {
		// Strip non-alphanumeric characters from both ends.
		w = strings.TrimFunc(w, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r >= 0x80) // keep unicode letters
		})
		if w == "" {
			continue
		}
		// Wrap in double quotes for literal FTS5 matching.
		clean = append(clean, `"`+w+`"`)
	}
	return strings.Join(clean, " ")
}

func MemoryVectorBlob(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		bits := math.Float32bits(f)
		binary.LittleEndian.PutUint32(out[4*i:], bits)
	}
	return out
}

func ScanMemoryItems(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}) ([]MemoryItem, error) {
	var items []MemoryItem
	for rows.Next() {
		var it MemoryItem
		var createdMs int64
		if err := rows.Scan(&it.ID, &it.ChatFolder, &it.Kind, &it.Title,
			&it.Content, &it.Source, &createdMs); err != nil {
			return nil, err
		}
		it.CreatedAt = time.UnixMilli(createdMs)
		items = append(items, it)
	}
	return items, rows.Err()
}
