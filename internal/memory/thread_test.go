package memory

import (
	"context"
	"hash/fnv"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// hashEmbedder is a deterministic fake: distinct texts get distinct,
// non-similar vectors so the dedup path never fires by accident.
type hashEmbedder struct{}

func (hashEmbedder) Model() string { return "fake" }
func (hashEmbedder) Dim() int      { return 1024 }
func (hashEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, t := range texts {
		h := fnv.New64a()
		h.Write([]byte(t))
		s := h.Sum64()
		v := make([]float32, 1024)
		for j := range v {
			v[j] = float32(int((s>>(uint(j%8)*8))&0xff)+j%7) - 128
		}
		out[i] = v
	}
	return out, nil
}

func newThreadTestMemory(t *testing.T) *Memory {
	t.Helper()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), log)
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return New(st, hashEmbedder{}, log)
}

func TestSummaryIsPerConversation(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	if _, err := m.AddInThread(ctx, "chat", 0, "conversation_summary", "", "MAIN-SUMMARY", "compact"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.AddInThread(ctx, "chat", 5, "conversation_summary", "", "THREAD5-SUMMARY", "compact"); err != nil {
		t.Fatal(err)
	}
	main := m.CompileContext(ctx, "chat", 0, "", DefaultBudget)
	th := m.CompileContext(ctx, "chat", 5, "", DefaultBudget)
	if !strings.Contains(main, "MAIN-SUMMARY") || strings.Contains(main, "THREAD5-SUMMARY") {
		t.Errorf("main context wrong: %q", main)
	}
	if !strings.Contains(th, "THREAD5-SUMMARY") || strings.Contains(th, "MAIN-SUMMARY") {
		t.Errorf("thread context wrong: %q", th)
	}
}

func TestSupersedeOnlyWithinConversation(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.AddInThread(ctx, "chat", 0, "conversation_summary", "", "main one", "compact")
	m.AddInThread(ctx, "chat", 5, "conversation_summary", "", "thread one", "compact")
	m.AddInThread(ctx, "chat", 5, "conversation_summary", "", "thread two", "compact")

	main := m.CompileContext(ctx, "chat", 0, "", DefaultBudget)
	if !strings.Contains(main, "main one") {
		t.Errorf("main summary was superseded by a thread write: %q", main)
	}
	th := m.CompileContext(ctx, "chat", 5, "", DefaultBudget)
	if strings.Contains(th, "thread one") || !strings.Contains(th, "thread two") {
		t.Errorf("thread supersede wrong: %q", th)
	}
}

func TestFactsAndPinnedAreSharedAcrossThreads(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	id, err := m.AddInThread(ctx, "chat", 5, "decision", "", "SHARED-DECISION", "agent")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.store.DB().ExecContext(ctx, `UPDATE memories SET pinned = 1 WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}
	m.Add(ctx, "chat", "decision", "", "MAIN-DECISION", "agent")
	for _, th := range []int{0, 5, 9} {
		got := m.CompileContext(ctx, "chat", th, "", DefaultBudget)
		if !strings.Contains(got, "SHARED-DECISION") {
			t.Errorf("thread %d missing pinned: %q", th, got)
		}
	}
}

func TestFallbackDoesNotLeakOtherThreadSummary(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.AddInThread(ctx, "chat", 0, "conversation_summary", "", "zebrafish MAINSUM", "compact")
	m.AddInThread(ctx, "chat", 5, "conversation_summary", "", "zebrafish THREADSUM", "compact")
	// Slots are under-filled, so the fallback search runs; FTS matches both.
	if hits, _ := m.Search(ctx, "chat", "zebrafish", 10); len(hits) < 2 {
		t.Fatalf("precondition: fallback search should match both summaries, got %d", len(hits))
	}
	main := m.CompileContext(ctx, "chat", 0, "zebrafish", DefaultBudget)
	if strings.Contains(main, "THREADSUM") || strings.Count(main, "MAINSUM") != 1 {
		t.Errorf("main context wrong: %q", main)
	}
	th := m.CompileContext(ctx, "chat", 5, "zebrafish", DefaultBudget)
	if strings.Contains(th, "MAINSUM") || strings.Count(th, "THREADSUM") != 1 {
		t.Errorf("thread context wrong: %q", th)
	}
}
