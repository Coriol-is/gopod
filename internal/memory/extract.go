package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"
)

// ExtractionPromptFn sends a prompt to Claude and returns the reply.
// Same callback type as control.PromptFunc — wired from main.go.
type ExtractionPromptFn func(ctx context.Context, folder, prompt string) (string, error)

// ExtractedFact is one structured fact extracted by the LLM from a
// conversation turn.
type ExtractedFact struct {
	Kind               string `json:"kind"`                          // fact, decision, preference, hypothesis
	Content            string `json:"content"`                       // normalized statement
	SupersedesKeyword  string `json:"supersedes_keyword,omitempty"`  // keyword to find + supersede old memory
}

const extractionPrompt = `You are a memory extraction system. Analyze the following conversation turn (user message + assistant reply) and extract ONLY durable, reusable facts.

Rules:
- Extract facts, decisions, preferences, and hypotheses that would be useful in FUTURE conversations
- Normalize each fact into a clear, standalone statement
- Set "kind" to exactly one of: "fact", "decision", "preference", "hypothesis"
- If a fact REPLACES a previous one (e.g. "changed language to Rust" replaces an old "uses Go"), set "supersedes_keyword" to a keyword that would find the old memory
- SKIP: greetings, filler, ephemeral questions, process discussion, things only relevant to this turn
- Return ONLY a JSON array. No markdown, no explanation. Empty array [] if nothing worth remembering.

User message: %s

Assistant reply: %s

JSON array:`

// ExtractFromTurn runs the extraction prompt against Claude and
// returns the parsed facts. Returns nil (not error) if no facts
// are extracted or if the LLM returns [].
func (m *Memory) ExtractFromTurn(
	ctx context.Context,
	chatFolder string,
	userMsg, agentReply string,
	promptFn ExtractionPromptFn,
) ([]ExtractedFact, error) {
	if promptFn == nil {
		return nil, nil
	}
	if len(userMsg) < 5 && len(agentReply) < 20 {
		// Skip very short exchanges (ping, hi, etc.)
		return nil, nil
	}

	prompt := fmt.Sprintf(extractionPrompt,
		truncateForPrompt(userMsg, 500),
		truncateForPrompt(agentReply, 1000))

	reply, err := promptFn(ctx, chatFolder, prompt)
	if err != nil {
		return nil, fmt.Errorf("memory: extraction: %w", err)
	}

	// Parse JSON array from reply.
	jsonStr := extractJSONArray(reply)
	if jsonStr == "" || jsonStr == "[]" {
		return nil, nil
	}

	var facts []ExtractedFact
	if err := json.Unmarshal([]byte(jsonStr), &facts); err != nil {
		m.log.Warn("memory: extraction parse failed",
			slog.String("reply", truncateForPrompt(reply, 200)),
			slog.Any("err", err))
		return nil, nil // don't fail the whole turn
	}

	// Filter out empty/invalid facts.
	var valid []ExtractedFact
	for _, f := range facts {
		if f.Content == "" {
			continue
		}
		switch f.Kind {
		case "fact", "decision", "preference", "hypothesis":
			// ok
		default:
			f.Kind = "fact" // normalize unknown kinds
		}
		valid = append(valid, f)
	}
	return valid, nil
}

// IngestExtracted stores extracted facts into memory, handling
// supersession and dedup.
func (m *Memory) IngestExtracted(ctx context.Context, chatFolder string, facts []ExtractedFact) {
	for _, f := range facts {
		// Handle supersession: if the fact declares it replaces
		// something, search for the old memory and mark it superseded.
		if f.SupersedesKeyword != "" {
			m.trySupersede(ctx, chatFolder, f.SupersedesKeyword)
		}

		id, err := m.Add(ctx, chatFolder, f.Kind, "", f.Content, "extraction")
		if err != nil {
			m.log.Warn("memory: ingest extracted fact failed",
				slog.String("content", truncateForPrompt(f.Content, 80)),
				slog.Any("err", err))
			continue
		}
		m.log.Debug("memory: extracted fact stored",
			slog.Int64("id", id),
			slog.String("kind", f.Kind),
			slog.String("content", truncateForPrompt(f.Content, 60)))
	}
}

// trySupersede searches for an existing active memory matching the
// keyword and supersedes it with the assumption that a newer fact
// is about to be stored.
func (m *Memory) trySupersede(ctx context.Context, chatFolder, keyword string) {
	// Use FTS for keyword match — more precise than vector for
	// supersession detection.
	rows, err := m.store.DB().QueryContext(ctx, `
		SELECT m.id FROM memory_fts f
		  JOIN memories m ON m.id = f.rowid
		 WHERE memory_fts MATCH ?
		   AND m.chat_folder = ?
		   AND IFNULL(m.status, 'active') = 'active'
		 LIMIT 1`, keyword, chatFolder)
	if err != nil {
		return
	}
	defer rows.Close()
	if !rows.Next() {
		return
	}
	var oldID int64
	if err := rows.Scan(&oldID); err != nil {
		return
	}
	// Mark as superseded (superseded_by will point to the new fact
	// once it's stored — for now just mark the status).
	m.store.DB().ExecContext(ctx, `
		UPDATE memories SET status = 'superseded', updated_at = ?
		 WHERE id = ?`, fmt.Sprintf("%d", timeNowMs()), oldID)
	m.log.Debug("memory: superseded old fact",
		slog.Int64("old_id", oldID),
		slog.String("keyword", keyword))
}

func timeNowMs() int64 {
	return time.Now().UnixMilli()
}

func extractJSONArray(s string) string {
	s = strings.TrimSpace(s)
	// Strip markdown fences.
	if strings.HasPrefix(s, "```") {
		if i := strings.Index(s[3:], "\n"); i >= 0 {
			s = s[3+i+1:]
		}
		if i := strings.LastIndex(s, "```"); i >= 0 {
			s = s[:i]
		}
		s = strings.TrimSpace(s)
	}
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start < 0 || end <= start {
		return ""
	}
	return s[start : end+1]
}

func truncateForPrompt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
