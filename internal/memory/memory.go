package memory

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
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
type Item = store.MemoryItem

// Add stores a new memory item, embeds its content, and inserts
// both the relational row and the vector. Returns the row ID.
func (m *Memory) Add(ctx context.Context, chatFolder, kind, title, content, source string) (int64, error) {
	return m.AddInThread(ctx, chatFolder, 0, kind, title, content, source)
}

// AddInThread is Add with a conversation (thread) scope: the row is
// stored with thread_id = thread for every kind (D023). Thread 0 is the
// main conversation and is what manual writes use; its rows are shared
// by every conversation of the chat. A row of thread T != 0 is visible
// only to T and to main (see visibleIn). conversation_summary rows are
// further limited to their own conversation's reserved slot.
func (m *Memory) AddInThread(ctx context.Context, chatFolder string, thread int, kind, title, content, source string) (int64, error) {
	return m.addInThread(ctx, chatFolder, thread, kind, title, content, source, "")
}

func (m *Memory) addInThread(ctx context.Context, chatFolder string, thread int, kind, title, content, source, keyword string) (int64, error) {
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
			vectorDim(vecs), m.embedder.Dim())
	}

	// Similarity selects only a replacement candidate; the store rechecks
	// provenance and pin status while inserting both row and vector atomically.
	var existingID int64
	if kind != "conversation_summary" {
		var sim float64
		existingID, sim, err = m.FindSimilar(ctx, chatFolder, thread, vecs[0])
		if err != nil {
			return 0, fmt.Errorf("memory: similar lookup: %w", err)
		}
		if sim <= 0.92 {
			existingID = 0
		}
	}
	id, err := m.store.InsertMemory(ctx, store.MemoryWrite{Folder: chatFolder, Thread: thread, Kind: kind, Title: title, Content: content, Source: source, Model: m.embedder.Model(), Vector: float32sToBlob(vecs[0]), SimilarID: existingID, Keyword: keyword})
	if err != nil {
		return 0, err
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
// Search is chat-wide (every thread, every summary); it backs the
// owner-facing control commands. Agent reads use SearchInThread.
func (m *Memory) Search(ctx context.Context, chatFolder, query string, k int) ([]Item, error) {
	return m.SearchInThread(ctx, chatFolder, allThreads, query, k)
}

// SearchInThread is Search limited to the rows conversation thread may
// see (visibleIn): main sees every thread's rows, a thread sees main's
// and its own, and summaries only ever of the own conversation.
func (m *Memory) SearchInThread(ctx context.Context, chatFolder string, thread int, query string, k int) ([]Item, error) {
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
	vecResults, err := m.vecSearch(ctx, chatFolder, thread, vecs[0], fetchK)
	if err != nil {
		m.log.Warn("memory: vec search failed, falling back to FTS only",
			slog.Any("err", err))
	}

	// FTS search.
	ftsResults, err := m.ftsSearch(ctx, chatFolder, thread, query, fetchK)
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

// ListInThread returns the most recent active memories conversation
// thread may see (visibleIn), newest first. It backs the agent-facing
// /memory/list; the owner-facing List stays chat-wide.
func (m *Memory) ListInThread(ctx context.Context, chatFolder string, thread int, limit int) ([]Item, error) {
	return m.store.ListMemoriesInThread(ctx, chatFolder, thread, limit)
}

// List returns the most recent memories for a chat, ordered by
// created_at descending. No embedding needed — this is a simple
// SQL scan. Chat-wide: owner-facing only.
func (m *Memory) List(ctx context.Context, chatFolder string, limit int) ([]Item, error) {
	return m.store.ListMemories(ctx, chatFolder, limit)
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
	return m.store.DeleteMemory(ctx, id)
}

// Supersede marks an existing memory as superseded by a newer one.
// The old memory stays in the DB for audit but is excluded from
// search results (status != 'active').
func (m *Memory) Supersede(ctx context.Context, oldID, newID int64) error {
	return m.store.SupersedeMemory(ctx, oldID, newID)
}

// touchRetrieved updates last_retrieved_at for a set of memory IDs.
// Called after search results are compiled into the agent prompt.
func (m *Memory) touchRetrieved(ctx context.Context, ids []int64) {
	if err := m.store.TouchMemories(ctx, ids); err != nil {
		m.log.Warn("memory: touch retrieved", "err", err)
	}
}

// similarCandidates is how many nearest neighbours FindSimilar takes
// from the vector index before filtering by chat and thread: vec0 picks
// the k nearest first and the JOIN filters after, so k = 1 would miss a
// same-conversation duplicate whenever another chat's or thread's row
// is nearer.
const similarCandidates = 32

// FindSimilar returns the most similar existing memory of the same
// conversation (chat_folder and thread_id) to vec (by cosine distance).
// Used for dedup before Add. Returns (id, similarity, error).
// Similarity > 0.92 suggests a duplicate.
func (m *Memory) FindSimilar(ctx context.Context, chatFolder string, thread int, vec []float32) (int64, float64, error) {
	return m.store.FindSimilarMemory(ctx, chatFolder, thread, vec)
}

// allThreads is the thread value for chat-wide, owner-facing reads: no
// visibility filter at all. Telegram thread ids are never negative.
const allThreads = -1

// visibleIn returns the SQL condition (with its args) limiting rows of
// alias to those conversation thread may see (D023): thread 0 (main)
// sees every row of the chat; thread T != 0 sees thread 0 rows and its
// own. In both cases a conversation_summary is only visible to its own
// conversation. allThreads means no filter. The condition starts with
// " AND " or is empty.
func visibleIn(alias string, thread int) (string, []any) {
	return store.MemoryVisibility(alias, thread)
}

// vecSearch runs a KNN query against memory_vec, filtering by
// chat_folder and thread visibility via a JOIN. Only returns active
// memories.
func (m *Memory) vecSearch(ctx context.Context, chatFolder string, thread int, queryVec []float32, k int) ([]Item, error) {
	return m.store.SearchMemoryVectors(ctx, chatFolder, thread, queryVec, k)
}

// ftsSearch runs a BM25 query against memory_fts, limited to the rows
// conversation thread may see (visibleIn).
func (m *Memory) ftsSearch(ctx context.Context, chatFolder string, thread int, query string, k int) ([]Item, error) {
	return m.store.SearchMemoryText(ctx, chatFolder, thread, query, k)
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
	return store.MemoryVectorBlob(v)
}

// sanitizeFTS turns arbitrary user text into a valid FTS5 query by
// extracting alphanumeric words and joining them with spaces. FTS5
// MATCH treats punctuation as query operators (AND/OR/NOT/NEAR);
// passing raw text with dots, commas, or question marks produces
// "fts5: syntax error near ..." errors. Wrapping each word in
// double quotes makes them literal phrase tokens.
func sanitizeFTS(s string) string {
	return store.SanitizeMemoryFTS(s)
}

// ArchiveStale marks memories not retrieved in archiveAfter days as
// status=archived. Returns count of archived items. Pinned memories
// are exempt. Only runs if total memory count > minCount (decay is
// pointless on a small store).
func (m *Memory) ArchiveStale(ctx context.Context, chatFolder string, archiveAfterDays, minCount int) (int64, error) {
	return m.store.ArchiveStaleMemories(ctx, chatFolder, archiveAfterDays, minCount)
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
	return store.ScanMemoryItems(rows)
}

func vectorDim(vecs [][]float32) int {
	if len(vecs) == 0 {
		return 0
	}
	return len(vecs[0])
}
