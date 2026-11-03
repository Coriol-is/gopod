package memory

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
)

// ExtractionPromptFn sends a prompt to Claude and returns the reply.
// Same callback type as control.PromptFunc — wired from main.go.
type ExtractionPromptFn func(ctx context.Context, folder, prompt string) (string, error)

// ExtractedFact is one structured fact extracted by the LLM from a
// conversation turn.
type ExtractedFact struct {
	Kind              string `json:"kind"`                         // fact, decision, preference, hypothesis
	Content           string `json:"content"`                      // normalized statement
	SupersedesKeyword string `json:"supersedes_keyword,omitempty"` // keyword to find + supersede old memory
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

// IngestExtracted stores facts extracted from a turn of conversation
// thread into that thread (D023), handling supersession and dedup
// within the same conversation.
func (m *Memory) IngestExtracted(ctx context.Context, chatFolder string, thread int, facts []ExtractedFact) {
	for _, f := range facts {
		id, err := m.addInThread(ctx, chatFolder, thread, f.Kind, "", f.Content, "extraction", sanitizeFTSKeyword(f.SupersedesKeyword))
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

// sanitizeFTSKeyword wraps a supersession keyword for FTS5 MATCH.
func sanitizeFTSKeyword(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	words := strings.Fields(s)
	var clean []string
	for _, w := range words {
		w = strings.TrimFunc(w, func(r rune) bool {
			return !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
				(r >= '0' && r <= '9') || r >= 0x80)
		})
		if w != "" {
			clean = append(clean, `"`+w+`"`)
		}
	}
	return strings.Join(clean, " ")
}

func truncateForPrompt(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}
