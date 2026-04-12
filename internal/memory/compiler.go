package memory

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// ContextBudget defines how many items of each kind the compiler
// selects for the agent's system prompt. Total = sum of all slots.
type ContextBudget struct {
	Summary     int // reserved slot for conversation_summary (most recent)
	Pinned      int // always-include pinned memories (any kind)
	Decisions   int // recent decisions (last 7 days)
	Preferences int // semantic-matched preferences
	Facts       int // semantic-matched facts
	Fallback    int // raw search if structured slots not filled
}

// DefaultBudget is the production budget. 6 items total.
var DefaultBudget = ContextBudget{
	Summary:     1, // guaranteed slot for conversation summary
	Pinned:      2,
	Decisions:   1,
	Preferences: 1,
	Facts:       1,
	Fallback:    1,
}

// CompileContext builds the system prompt appendix for one agent turn.
// Instead of flat top-k cosine search, it fills slots by kind with
// policies (pinned first, then recent decisions, then query-relevant
// preferences and facts). Only if the structured slots under-fill
// does it use a raw semantic fallback.
//
// Returns "" if no relevant memories exist (agent runs context-free).
func (m *Memory) CompileContext(ctx context.Context, chatFolder, query string, budget ContextBudget) string {
	var selected []Item

	// 0. Conversation summary (reserved slot — guaranteed to be included
	// if one exists). This is the bridge after session compact.
	if budget.Summary > 0 {
		summaries := m.fetchRecentByKind(ctx, chatFolder, "conversation_summary", 30, budget.Summary)
		selected = append(selected, summaries...)
	}

	// 1. Pinned memories (always included regardless of query).
	if budget.Pinned > 0 {
		pinned := m.fetchPinned(ctx, chatFolder, budget.Pinned)
		selected = append(selected, pinned...)
	}

	// 2. Recent decisions (last 7 days).
	if budget.Decisions > 0 {
		decisions := m.fetchRecentByKind(ctx, chatFolder, "decision", 7, budget.Decisions)
		selected = append(selected, dedupItems(selected, decisions)...)
	}

	// 3. Query-relevant preferences.
	if budget.Preferences > 0 && query != "" {
		prefs := m.fetchByKindSemantic(ctx, chatFolder, "preference", query, budget.Preferences)
		selected = append(selected, dedupItems(selected, prefs)...)
	}

	// 4. Query-relevant facts.
	if budget.Facts > 0 && query != "" {
		facts := m.fetchByKindSemantic(ctx, chatFolder, "fact", query, budget.Facts)
		selected = append(selected, dedupItems(selected, facts)...)
	}

	// 5. Fallback: if structured slots are underfilled, do a broad
	// semantic search to fill remaining capacity.
	total := budget.Pinned + budget.Decisions + budget.Preferences + budget.Facts
	if len(selected) < total && budget.Fallback > 0 && query != "" {
		remaining := total - len(selected) + budget.Fallback
		fallback, _ := m.Search(ctx, chatFolder, query, remaining)
		for _, f := range fallback {
			selected = append(selected, dedupItems(selected, []Item{f})...)
		}
	}

	if len(selected) == 0 {
		return ""
	}

	return formatContextBlock(selected)
}

func (m *Memory) fetchPinned(ctx context.Context, chatFolder string, limit int) []Item {
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT id, chat_folder, kind, IFNULL(title,''), content, IFNULL(source,''), created_at
		  FROM memories
		 WHERE chat_folder = ?
		   AND IFNULL(status,'active') = 'active'
		   AND pinned = 1
		 ORDER BY created_at DESC
		 LIMIT ?`, chatFolder, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items, _ := scanItems(rows)
	return items
}

func (m *Memory) fetchRecentByKind(ctx context.Context, chatFolder, kind string, days, limit int) []Item {
	cutoff := time.Now().Add(-time.Duration(days) * 24 * time.Hour).UnixMilli()
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT id, chat_folder, kind, IFNULL(title,''), content, IFNULL(source,''), created_at
		  FROM memories
		 WHERE chat_folder = ?
		   AND IFNULL(status,'active') = 'active'
		   AND kind = ?
		   AND created_at >= ?
		 ORDER BY created_at DESC
		 LIMIT ?`, chatFolder, kind, cutoff, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items, _ := scanItems(rows)
	return items
}

func (m *Memory) fetchByKindSemantic(ctx context.Context, chatFolder, kind, query string, limit int) []Item {
	// Embed the query, then KNN search filtered by kind.
	vecs, err := m.embedder.Embed(ctx, []string{query})
	if err != nil || len(vecs) == 0 {
		return nil
	}
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id, m.chat_folder, m.kind, IFNULL(m.title,''), m.content,
		       IFNULL(m.source,''), m.created_at, v.distance
		  FROM memory_vec v
		  JOIN memories m ON m.id = v.rowid
		 WHERE v.embedding MATCH ?
		   AND v.k = ?
		   AND m.chat_folder = ?
		   AND m.kind = ?
		   AND IFNULL(m.status,'active') = 'active'
		 ORDER BY v.distance ASC`,
		float32sToBlob(vecs[0]), limit*3, chatFolder, kind)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var items []Item
	for rows.Next() {
		var it Item
		var createdMs int64
		var dist float64
		if err := rows.Scan(&it.ID, &it.ChatFolder, &it.Kind, &it.Title,
			&it.Content, &it.Source, &createdMs, &dist); err != nil {
			continue
		}
		it.CreatedAt = time.UnixMilli(createdMs)
		it.Score = 1.0 / (1.0 + dist)
		items = append(items, it)
		if len(items) >= limit {
			break
		}
	}
	return items
}

// dedupItems returns only items from candidates whose IDs are not
// already in existing.
func dedupItems(existing, candidates []Item) []Item {
	seen := make(map[int64]bool, len(existing))
	for _, it := range existing {
		seen[it.ID] = true
	}
	var out []Item
	for _, it := range candidates {
		if !seen[it.ID] {
			out = append(out, it)
			seen[it.ID] = true
		}
	}
	return out
}

func formatContextBlock(items []Item) string {
	var sb strings.Builder
	sb.WriteString("Here are relevant facts from previous conversations with this user. Use them to personalize your response when relevant, but do not mention that you are reading from a memory system unless asked:\n\n")
	for i, it := range items {
		prefix := fmt.Sprintf("%d. [%s]", i+1, it.Kind)
		if it.Title != "" {
			fmt.Fprintf(&sb, "%s %s: %s\n", prefix, it.Title, it.Content)
		} else {
			fmt.Fprintf(&sb, "%s %s\n", prefix, it.Content)
		}
	}
	return sb.String()
}
