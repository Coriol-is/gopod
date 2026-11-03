package memory

import (
	"context"
	"errors"
	"github.com/Coriol-is/gopod/internal/store"
	"testing"
)

func TestReplacementProtectsProvenanceAndPreservesHistory(t *testing.T) {
	for _, source := range []string{"manual", "user", "agent", "extraction", ""} {
		for _, pinned := range []bool{false, true} {
			t.Run(source+map[bool]string{false: "", true: "-pinned"}[pinned], func(t *testing.T) {
				m := newThreadTestMemory(t)
				ctx := t.Context()
				old, err := m.Add(ctx, "chat", "fact", "old title", "Lisbon", source)
				if err != nil {
					t.Fatal(err)
				}
				if pinned {
					if _, err = m.store.DB().ExecContext(ctx, `UPDATE memories SET pinned=1 WHERE id=?`, old); err != nil {
						t.Fatal(err)
					}
				}
				m.IngestExtracted(ctx, "chat", 0, []ExtractedFact{{Kind: "decision", Content: "Lisbon", SupersedesKeyword: "Lisbon"}})
				var content, kind, gotSource, status string
				var by int64
				if err := m.store.DB().QueryRowContext(ctx, `SELECT content,kind,source,status,IFNULL(superseded_by,0) FROM memories WHERE id=?`, old).Scan(&content, &kind, &gotSource, &status, &by); err != nil {
					t.Fatal(err)
				}
				if content != "Lisbon" || kind != "fact" || gotSource != source {
					t.Fatal("original rewritten")
				}
				eligible := !pinned && (source == "agent" || source == "extraction")
				if eligible && (status != "superseded" || by == 0) {
					t.Fatalf("replacement missing: %s %d", status, by)
				}
				if !eligible && (status != "active" || by != 0) {
					t.Fatalf("protected row replaced: %s %d", status, by)
				}
			})
		}
	}
}

type failingEmbedder struct{ hashEmbedder }

func (failingEmbedder) Embed(context.Context, []string) ([][]float32, error) {
	return nil, errors.New("embedding unavailable")
}
func TestFailedReplacementKeepsOriginalActive(t *testing.T) {
	m := newThreadTestMemory(t)
	old, err := m.Add(t.Context(), "chat", "fact", "", "Lisbon", "extraction")
	if err != nil {
		t.Fatal(err)
	}
	m.embedder = failingEmbedder{}
	m.IngestExtracted(t.Context(), "chat", 0, []ExtractedFact{{Kind: "fact", Content: "Porto", SupersedesKeyword: "Lisbon"}})
	var state string
	if err := m.store.DB().QueryRow(`SELECT status FROM memories WHERE id=?`, old).Scan(&state); err != nil || state != "active" {
		t.Fatalf("%s %v", state, err)
	}
}

func TestVectorFailureRollsBackReplacement(t *testing.T) {
	m := newThreadTestMemory(t)
	old, err := m.Add(t.Context(), "chat", "fact", "", "Lisbon", "extraction")
	if err != nil {
		t.Fatal(err)
	}
	// Invalid vector dimension fails after relational insert within InsertMemory.
	_, err = m.store.InsertMemory(t.Context(), store.MemoryWrite{Folder: "chat", Kind: "fact", Source: "extraction", Content: "Porto", Model: "fake", Vector: []byte{0, 0, 0, 0}, SimilarID: old, Keyword: "Lisbon"})
	if err == nil {
		t.Fatal("invalid vector accepted")
	}

	var count int
	if err := m.store.DB().QueryRow(`SELECT count(*) FROM memories`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rows=%d %v", count, err)
	}
	var state string
	if err := m.store.DB().QueryRow(`SELECT status FROM memories WHERE id=?`, old).Scan(&state); err != nil || state != "active" {
		t.Fatalf("%s %v", state, err)
	}
}

func TestSummaryReplacementProtectsManualAndUnknownSources(t *testing.T) {
	for _, source := range []string{"manual", "user", "", "compact"} {
		t.Run(source, func(t *testing.T) {
			m := newThreadTestMemory(t)
			old, err := m.Add(t.Context(), "chat", "conversation_summary", "", "old summary", source)
			if err != nil {
				t.Fatal(err)
			}
			next, err := m.Add(t.Context(), "chat", "conversation_summary", "", "new summary", "compact")
			if err != nil {
				t.Fatal(err)
			}
			var state string
			var by int64
			if err := m.store.DB().QueryRow(`SELECT status,IFNULL(superseded_by,0) FROM memories WHERE id=?`, old).Scan(&state, &by); err != nil {
				t.Fatal(err)
			}
			if source == "compact" {
				if state != "superseded" || by != next {
					t.Fatalf("%s %d", state, by)
				}
			} else if state != "active" || by != 0 {
				t.Fatalf("protected source %s changed: %s %d", source, state, by)
			}
		})
	}
}
