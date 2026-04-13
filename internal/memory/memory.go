package memory

import (
	"context"
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/spaceinvaderz/gopod/internal/store"
)

// Memory is the high-level facade for gopod's long-term memory
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
	ID              int64
	ChatFolder      string
	Kind            string
	Title           string
	Content         string
	Source          string
	Score           float64   // combined score from hybrid search
	CreatedAt       time.Time
	LastRetrievedAt time.Time // zero if never retrieved
	Pinned          bool
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

	// For conversation_summary: supersede the previous one so they
	// don't accumulate. Each chat should have at most one active summary.
	if kind == "conversation_summary" {
		m.supersedePreviousSummary(ctx, chatFolder)
	}

	// Dedup: if a very similar active memory already exists (cosine
	// similarity > 0.92), update it instead of creating a duplicate.
	existingID, sim, err := m.FindSimilar(ctx, chatFolder, vecs[0])
	if err == nil && existingID > 0 && sim > 0.92 {
		now := time.Now().UnixMilli()
		_, err := m.store.DB().ExecContext(ctx, `
			UPDATE memories SET content = ?, kind = ?, updated_at = ?
			 WHERE id = ?`, content, kind, now, existingID)
		if err != nil {
			return 0, fmt.Errorf("memory: Add: dedup update: %w", err)
		}
		// Update the vector too.
		m.store.DB().ExecContext(ctx, `
			UPDATE memory_vec SET embedding = ? WHERE rowid = ?`,
			float32sToBlob(vecs[0]), existingID)
		m.log.Debug("memory: dedup update",
			slog.Int64("id", existingID),
			slog.Float64("similarity", sim))
		return existingID, nil
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

	// Update last_retrieved_at for decay scoring.
	ids := make([]int64, len(merged))
	for i, it := range merged {
		ids[i] = it.ID
	}
	m.touchRetrieved(ctx, ids)

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

// Supersede marks an existing memory as superseded by a newer one.
// The old memory stays in the DB for audit but is excluded from
// search results (status != 'active').
func (m *Memory) Supersede(ctx context.Context, oldID, newID int64) error {
	_, err := m.store.DB().ExecContext(ctx, `
		UPDATE memories SET status = 'superseded', superseded_by = ?, updated_at = ?
		 WHERE id = ?`, newID, time.Now().UnixMilli(), oldID)
	return err
}

// touchRetrieved updates last_retrieved_at for a set of memory IDs.
// Called after search results are compiled into the agent prompt.
func (m *Memory) touchRetrieved(ctx context.Context, ids []int64) {
	if len(ids) == 0 {
		return
	}
	now := time.Now().UnixMilli()
	for _, id := range ids {
		m.store.DB().ExecContext(ctx, `UPDATE memories SET last_retrieved_at = ? WHERE id = ?`, now, id)
	}
}

// FindSimilar returns the most similar existing memory to content
// (by cosine distance). Used for dedup before Add. Returns (id,
// similarity, error). Similarity > 0.92 suggests a duplicate.
func (m *Memory) FindSimilar(ctx context.Context, chatFolder string, vec []float32) (int64, float64, error) {
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = 1
		   AND m.chat_folder = ?
		   AND IFNULL(m.status, 'active') = 'active'`,
		float32sToBlob(vec), chatFolder)
	if err != nil {
		return 0, 0, err
	}
	defer rows.Close()
	if !rows.Next() {
		return 0, 0, nil // no existing memories
	}
	var id int64
	var dist float64
	if err := rows.Scan(&id, &dist); err != nil {
		return 0, 0, err
	}
	similarity := 1.0 / (1.0 + dist)
	return id, similarity, nil
}

// vecSearch runs a KNN query against memory_vec, filtering by
// chat_folder via a JOIN. Only returns active memories.
func (m *Memory) vecSearch(ctx context.Context, chatFolder string, queryVec []float32, k int) ([]Item, error) {
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, IFNULL(m.last_retrieved_at,0),
		       IFNULL(m.pinned,0), v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = ?
		   AND (? = '' OR m.chat_folder = ?)
		   AND IFNULL(m.status, 'active') = 'active'
		 ORDER BY v.distance ASC`,
		float32sToBlob(queryVec), k, chatFolder, chatFolder)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
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

// ftsSearch runs a BM25 query against memory_fts.
func (m *Memory) ftsSearch(ctx context.Context, chatFolder, query string, k int) ([]Item, error) {
	// Sanitize query for FTS5: wrap each word in double quotes so
	// punctuation (.,?!) doesn't break FTS5 query syntax. FTS5 MATCH
	// treats raw punctuation as operators and errors on them.
	sanitized := sanitizeFTS(query)
	if sanitized == "" {
		return nil, nil
	}
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, rank
		  FROM memory_fts f
		  JOIN memories m ON m.id = f.rowid
		 WHERE memory_fts MATCH ?
		   AND (? = '' OR m.chat_folder = ?)
		   AND IFNULL(m.status, 'active') = 'active'
		 ORDER BY rank ASC
		 LIMIT ?`, sanitized, chatFolder, chatFolder, k)
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

// rrfMerge combines two ranked lists using Reciprocal Rank Fusion
// with decay scoring. k=60 is the standard RRF constant.
//
// Decay: memories not retrieved in the last 7 days get a penalty
// that increases with staleness. Pinned memories are exempt.
// Recently retrieved memories get a small boost.
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

	// Apply decay multiplier.
	now := time.Now()
	for id, score := range scores {
		it := items[id]
		scores[id] = score * decayMultiplier(it, now)
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

// decayMultiplier returns a score multiplier based on how recently
// a memory was retrieved. Range: 0.3 (very stale) to 1.2 (fresh).
//
// - Retrieved in last 24h: 1.2x boost
// - Retrieved in last 7 days: 1.0x (no change)
// - Not retrieved in 7-30 days: 0.7x penalty
// - Not retrieved in 30+ days: 0.5x penalty
// - Never retrieved (last_retrieved_at zero): 0.8x (new memory, slight penalty)
// - Pinned: always 1.0x (exempt from decay)
func decayMultiplier(it Item, now time.Time) float64 {
	if it.Pinned {
		return 1.0
	}
	if it.LastRetrievedAt.IsZero() {
		// Never retrieved — new memory, slight penalty so established
		// memories rank higher.
		daysSinceCreated := now.Sub(it.CreatedAt).Hours() / 24
		if daysSinceCreated < 1 {
			return 1.1 // very fresh, boost
		}
		return 0.8
	}

	daysSinceRetrieved := now.Sub(it.LastRetrievedAt).Hours() / 24

	switch {
	case daysSinceRetrieved < 1:
		return 1.2
	case daysSinceRetrieved < 7:
		return 1.0
	case daysSinceRetrieved < 30:
		return 0.7
	default:
		return 0.5
	}
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

// sanitizeFTS turns arbitrary user text into a valid FTS5 query by
// extracting alphanumeric words and joining them with spaces. FTS5
// MATCH treats punctuation as query operators (AND/OR/NOT/NEAR);
// passing raw text with dots, commas, or question marks produces
// "fts5: syntax error near ..." errors. Wrapping each word in
// double quotes makes them literal phrase tokens.
func sanitizeFTS(s string) string {
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

// supersedePreviousSummary marks all existing active conversation_summary
// entries for a chat as superseded, so only the newest one remains active.
func (m *Memory) supersedePreviousSummary(ctx context.Context, chatFolder string) {
	m.store.DB().ExecContext(ctx, `
		UPDATE memories SET status = 'superseded', updated_at = ?
		 WHERE chat_folder = ? AND kind = 'conversation_summary'
		   AND IFNULL(status, 'active') = 'active'`,
		time.Now().UnixMilli(), chatFolder)
}

// ArchiveStale marks memories not retrieved in archiveAfter days as
// status=archived. Returns count of archived items. Pinned memories
// are exempt. Only runs if total memory count > minCount (decay is
// pointless on a small store).
func (m *Memory) ArchiveStale(ctx context.Context, chatFolder string, archiveAfterDays, minCount int) (int64, error) {
	if archiveAfterDays <= 0 {
		archiveAfterDays = 90
	}
	if minCount <= 0 {
		minCount = 50
	}

	// Check if we have enough memories to justify archiving.
	var count int64
	m.store.DB().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM memories WHERE chat_folder = ? AND IFNULL(status,'active') = 'active'`,
		chatFolder).Scan(&count)
	if count < int64(minCount) {
		return 0, nil // too few memories, skip
	}

	cutoff := time.Now().Add(-time.Duration(archiveAfterDays) * 24 * time.Hour).UnixMilli()
	res, err := m.store.DB().ExecContext(ctx, `
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
