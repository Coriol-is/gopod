package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"time"

	"github.com/spaceinvaderz/picoclaw/internal/store"
)

// Memory is the high-level facade for picoclaw's long-term memory
// layer. It combines the relational store (memories table), the
// vector index (memory_vec), the FTS5 index (memory_fts), and the
// embedder into a single API.
type Memory struct {
	store    *store.Store
	embedder Embedder
	log      *slog.Logger
}

// New creates a Memory. The embedder is required — without it we
// cannot vectorize text for Add or Search.
func New(st *store.Store, embedder Embedder, log *slog.Logger) *Memory {
	if log == nil {
		log = slog.Default()
	}
	return &Memory{store: st, embedder: embedder, log: log}
}

// Item is a memory record returned by Search / List.
type Item struct {
	ID         int64
	ChatFolder string
	Kind       string
	Title      string
	Content    string
	Source     string
	Score      float64 // combined score from hybrid search (higher = more relevant)
	CreatedAt  time.Time
}

// Add stores a new memory item, embeds its content, and inserts
// both the relational row and the vector. Returns the row ID.
func (m *Memory) Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error) {
	if content == "" {
		return 0, fmt.Errorf("memory: Add: empty content")
	}
	if kind == "" {
		kind = "fact"
	}

	// Embed the content.
	vecs, err := m.embedder.Embed(ctx, []string{content})
	if err != nil {
		return 0, fmt.Errorf("memory: Add: embed: %w", err)
	}
	if len(vecs) == 0 || len(vecs[0]) != m.embedder.Dim() {
		return 0, fmt.Errorf("memory: Add: unexpected vector dim %d (want %d)",
			len(vecs[0]), m.embedder.Dim())
	}

	now := time.Now().UnixMilli()
	db := m.store.DB()

	// Insert relational row.
	res, err := db.ExecContext(ctx, `
		INSERT INTO memories (chat_folder, kind, source, title, content, embed_model, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		chatFolder, kind, source, nullableStr(title), content,
		m.embedder.Model(), now, now)
	if err != nil {
		return 0, fmt.Errorf("memory: Add: insert: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("memory: Add: last id: %w", err)
	}

	// Insert vector.
	if _, err := db.ExecContext(ctx, `
		INSERT INTO memory_vec (rowid, embedding) VALUES (?, ?)`,
		id, float32sToBlob(vecs[0])); err != nil {
		return 0, fmt.Errorf("memory: Add: vec insert: %w", err)
	}

	m.log.Debug("memory: added",
		slog.Int64("id", id),
		slog.String("chat", chatFolder),
		slog.String("kind", kind),
		slog.Int("content_len", len(content)))
	return id, nil
}

// Search does hybrid retrieval: semantic (vec0 KNN) + lexical (FTS5
// BM25), then merges results via Reciprocal Rank Fusion (RRF).
// Returns at most k items, highest score first.
//
// chatFolder scopes the search to one chat. Pass "_global" to search
// only global memories, or "" to search ALL memories (owner use case).
func (m *Memory) Search(ctx context.Context, chatFolder, query string, k int) ([]Item, error) {
	if query == "" {
		return nil, nil
	}
	if k <= 0 {
		k = 5
	}

	// Embed the query for vector search.
	vecs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil {
		return nil, fmt.Errorf("memory: Search: embed query: %w", err)
	}

	fetchK := k * 3 // over-fetch for RRF merge

	// Vector search.
	vecResults, err := m.vecSearch(ctx, chatFolder, vecs[0], fetchK)
	if err != nil {
		m.log.Warn("memory: vec search failed, falling back to FTS only",
			slog.Any("err", err))
	}

	// FTS search.
	ftsResults, err := m.ftsSearch(ctx, chatFolder, query, fetchK)
	if err != nil {
		m.log.Warn("memory: FTS search failed, falling back to vec only",
			slog.Any("err", err))
	}

	// RRF merge.
	merged := rrfMerge(vecResults, ftsResults, k)
	return merged, nil
}

// List returns the most recent memories for a chat, ordered by
// created_at descending. No embedding needed — this is a simple
// SQL scan.
func (m *Memory) List(ctx context.Context, chatFolder string, limit int) ([]Item, error) {
	if limit <= 0 {
		limit = 20
	}
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT id, chat_folder, kind, IFNULL(title,''), content, IFNULL(source,''), created_at
		  FROM memories
		 WHERE chat_folder = ?
		 ORDER BY created_at DESC
		 LIMIT ?`, chatFolder, limit)
	if err != nil {
		return nil, fmt.Errorf("memory: List: %w", err)
	}
	defer rows.Close()
	return scanItems(rows)
}

// SearchText is a convenience wrapper around Search that returns
// results in a shape the control package can consume without importing
// memory.Item directly.
func (m *Memory) SearchText(ctx context.Context, chatFolder, query string, k int) ([]SearchResult, error) {
	items, err := m.Search(ctx, chatFolder, query, k)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, len(items))
	for i, it := range items {
		out[i] = SearchResult{
			ID: it.ID, Kind: it.Kind, Title: it.Title,
			Content: it.Content, Score: it.Score,
		}
	}
	return out, nil
}

// SearchResult is the external-facing search result type.
type SearchResult struct {
	ID      int64
	Kind    string
	Title   string
	Content string
	Score   float64
}

// Delete removes a memory by ID (relational + vector + FTS via trigger).
func (m *Memory) Delete(ctx context.Context, id int64) error {
	db := m.store.DB()
	if _, err := db.ExecContext(ctx, `DELETE FROM memory_vec WHERE rowid = ?`, id); err != nil {
		return fmt.Errorf("memory: Delete vec: %w", err)
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM memories WHERE id = ?`, id); err != nil {
		return fmt.Errorf("memory: Delete: %w", err)
	}
	return nil
}

// vecSearch runs a KNN query against memory_vec, filtering by
// chat_folder via a JOIN.
func (m *Memory) vecSearch(ctx context.Context, chatFolder string, queryVec []float32, k int) ([]Item, error) {
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = ?
		   AND (? = '' OR m.chat_folder = ?)
		 ORDER BY v.distance ASC`,
		float32sToBlob(queryVec), k, chatFolder, chatFolder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
		var createdMs int64
		var dist float64
		if err := rows.Scan(&it.ID, &it.ChatFolder, &it.Kind, &it.Title,
			&it.Content, &it.Source, &createdMs, &dist); err != nil {
			return nil, err
		}
		it.CreatedAt = time.UnixMilli(createdMs)
		it.Score = 1.0 / (1.0 + dist) // convert distance to similarity
		items = append(items, it)
	}
	return items, rows.Err()
}

// ftsSearch runs a BM25 query against memory_fts.
func (m *Memory) ftsSearch(ctx context.Context, chatFolder, query string, k int) ([]Item, error) {
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, rank
		  FROM memory_fts f
		  JOIN memories m ON m.id = f.rowid
		 WHERE memory_fts MATCH ?
		   AND (? = '' OR m.chat_folder = ?)
		 ORDER BY rank ASC
		 LIMIT ?`, query, chatFolder, chatFolder, k)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
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

// rrfMerge combines two ranked lists using Reciprocal Rank Fusion.
// k=60 is the standard RRF constant.
func rrfMerge(vecResults, ftsResults []Item, topK int) []Item {
	const rrfK = 60.0
	scores := make(map[int64]float64)
	items := make(map[int64]Item)

	for rank, it := range vecResults {
		scores[it.ID] += 1.0 / (rrfK + float64(rank+1))
		items[it.ID] = it
	}
	for rank, it := range ftsResults {
		scores[it.ID] += 1.0 / (rrfK + float64(rank+1))
		if _, ok := items[it.ID]; !ok {
			items[it.ID] = it
		}
	}

	type scored struct {
		item  Item
		score float64
	}
	var merged []scored
	for id, score := range scores {
		it := items[id]
		it.Score = score
		merged = append(merged, scored{it, score})
	}
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].score > merged[j].score
	})

	result := make([]Item, 0, topK)
	for i, s := range merged {
		if i >= topK {
			break
		}
		result = append(result, s.item)
	}
	return result
}

// float32sToBlob serializes a float32 slice to little-endian bytes
// (the format sqlite-vec expects).
func float32sToBlob(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		bits := math.Float32bits(f)
		binary.LittleEndian.PutUint32(out[4*i:], bits)
	}
	return out
}

func nullableStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func scanItems(rows interface {
	Next() bool
	Scan(dest ...interface{}) error
	Err() error
}) ([]Item, error) {
	var items []Item
	for rows.Next() {
		var it Item
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
