package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// Visibility rule (D023): conversation T sees rows of thread 0 and of
// T; main (T = 0) sees every row of the chat. A row born in thread A is
// never visible to a sibling thread B.

const (
	threadA = 7
	threadB = 9
)

// slotCase isolates one CompileContext read path with a budget that
// only fills that path.
type slotCase struct {
	name   string
	kind   string
	pinned bool
	budget ContextBudget
}

func slotCases() []slotCase {
	return []slotCase{
		{name: "pinned", kind: "fact", pinned: true, budget: ContextBudget{Pinned: 2}},
		{name: "decisions", kind: "decision", budget: ContextBudget{Decisions: 1}},
		{name: "preferences", kind: "preference", budget: ContextBudget{Preferences: 1}},
		{name: "facts", kind: "fact", budget: ContextBudget{Facts: 1}},
		// hypothesis has no structured slot: only the fallback Search
		// (vec + FTS) can return it. Decisions:1 leaves room for it.
		{name: "fallback", kind: "hypothesis", budget: ContextBudget{Decisions: 1, Fallback: 1}},
	}
}

// addRow stores one row of kind in thread and pins it if asked.
func addRow(t *testing.T, m *Memory, thread int, c slotCase, content string) int64 {
	t.Helper()
	ctx := context.Background()
	id, err := m.AddInThread(ctx, "chat", thread, c.kind, "", content, "extraction")
	if err != nil {
		t.Fatalf("AddInThread: %v", err)
	}
	if c.pinned {
		if _, err := m.store.DB().ExecContext(ctx, `UPDATE memories SET pinned = 1 WHERE id = ?`, id); err != nil {
			t.Fatal(err)
		}
	}
	return id
}

// checkVisibility stores one row born in thread `born` and checks which
// conversations see it through the slot under test.
func checkVisibility(t *testing.T, born int, viewers map[int]bool) {
	t.Helper()
	for _, c := range slotCases() {
		t.Run(c.name, func(t *testing.T) {
			m := newThreadTestMemory(t)
			marker := fmt.Sprintf("ROW-%s-BORN%d", c.name, born)
			addRow(t, m, born, c, marker+" zebra")
			for viewer, want := range viewers {
				got := m.CompileContext(context.Background(), "chat", viewer, "zebra", c.budget)
				if strings.Contains(got, marker) != want {
					t.Errorf("thread %d sees %s = %v, want %v; context %q",
						viewer, marker, !want, want, got)
				}
			}
		})
	}
}

func TestThreadFactNotVisibleInSiblingThread(t *testing.T) {
	checkVisibility(t, threadA, map[int]bool{threadB: false})
}

func TestThreadFactVisibleInOwnThreadAndMain(t *testing.T) {
	checkVisibility(t, threadA, map[int]bool{threadA: true, 0: true})
}

func TestMainFactVisibleInThreads(t *testing.T) {
	checkVisibility(t, 0, map[int]bool{0: true, threadA: true, threadB: true})
}

func TestMainSeesThreadFacts(t *testing.T) {
	// Main is the chat-wide aggregate: rows of two different threads
	// both reach it.
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.AddInThread(ctx, "chat", threadA, "decision", "", "DEC-A", "extraction")
	m.AddInThread(ctx, "chat", threadB, "decision", "", "DEC-B", "extraction")
	got := m.CompileContext(ctx, "chat", 0, "", ContextBudget{Decisions: 2})
	if !strings.Contains(got, "DEC-A") || !strings.Contains(got, "DEC-B") {
		t.Errorf("main context lacks thread rows: %q", got)
	}
}

// A sibling row must not take a slot from a visible one: the filter is
// in the query, not applied after the LIMIT.
func TestSiblingRowDoesNotCrowdOutVisible(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.AddInThread(ctx, "chat", 0, "decision", "", "DEC-MAIN", "manual")
	m.AddInThread(ctx, "chat", threadA, "decision", "", "DEC-A-NEWER", "extraction")
	got := m.CompileContext(ctx, "chat", threadB, "", ContextBudget{Decisions: 1})
	if !strings.Contains(got, "DEC-MAIN") || strings.Contains(got, "DEC-A-NEWER") {
		t.Errorf("thread %d decisions slot wrong: %q", threadB, got)
	}
}

func TestFallbackSearchRespectsVisibility(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	idMain, _ := m.AddInThread(ctx, "chat", 0, "hypothesis", "", "quokka main", "manual")
	idA, _ := m.AddInThread(ctx, "chat", threadA, "hypothesis", "", "quokka in A", "extraction")
	idB, _ := m.AddInThread(ctx, "chat", threadB, "hypothesis", "", "quokka in B", "extraction")

	vec, err := m.embedder.Embed(ctx, []string{"quokka"})
	if err != nil {
		t.Fatal(err)
	}
	want := map[int]map[int64]bool{
		0:       {idMain: true, idA: true, idB: true},
		threadA: {idMain: true, idA: true},
		threadB: {idMain: true, idB: true},
	}
	for viewer, ids := range want {
		vecHits, err := m.vecSearch(ctx, "chat", viewer, vec[0], 10)
		if err != nil {
			t.Fatal(err)
		}
		ftsHits, err := m.ftsSearch(ctx, "chat", viewer, "quokka", 10)
		if err != nil {
			t.Fatal(err)
		}
		for path, hits := range map[string][]Item{"vec": vecHits, "fts": ftsHits} {
			got := map[int64]bool{}
			for _, h := range hits {
				got[h.ID] = true
			}
			if fmt.Sprint(got) != fmt.Sprint(ids) {
				t.Errorf("%s search from thread %d = %v, want %v", path, viewer, got, ids)
			}
		}
	}

	// And end to end: a non-empty query reaching the fallback.
	got := m.CompileContext(ctx, "chat", threadA, "quokka", ContextBudget{Decisions: 1, Fallback: 2})
	if !strings.Contains(got, "quokka main") || !strings.Contains(got, "quokka in A") || strings.Contains(got, "quokka in B") {
		t.Errorf("thread %d fallback context wrong: %q", threadA, got)
	}
}

func TestDedupDoesNotCrossThreads(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	const text = "the owner lives in Lisbon"
	kindOf := func(id int64) (kind string, thread int) {
		t.Helper()
		if err := m.store.DB().QueryRowContext(ctx,
			`SELECT kind, thread_id FROM memories WHERE id = ?`, id).Scan(&kind, &thread); err != nil {
			t.Fatal(err)
		}
		return
	}

	mainID, _ := m.Add(ctx, "chat", "fact", "", text, "user")
	aID, err := m.AddInThread(ctx, "chat", threadA, "fact", "", text, "extraction")
	if err != nil {
		t.Fatal(err)
	}
	if aID == mainID {
		t.Fatalf("thread %d fact deduped into the main row", threadA)
	}
	// Same text again in A, as a decision: updates A's row only.
	again, _ := m.AddInThread(ctx, "chat", threadA, "decision", "", text, "extraction")
	if again != aID {
		t.Errorf("same-thread duplicate not deduped: got %d, want %d", again, aID)
	}
	if k, th := kindOf(aID); k != "decision" || th != threadA {
		t.Errorf("thread row = (%s, %d), want (decision, %d)", k, th, threadA)
	}
	if k, th := kindOf(mainID); k != "fact" || th != 0 {
		t.Errorf("main row changed to (%s, %d)", k, th)
	}
	bID, _ := m.AddInThread(ctx, "chat", threadB, "fact", "", text, "extraction")
	if bID == aID || bID == mainID {
		t.Errorf("thread %d fact deduped into another conversation's row %d", threadB, bID)
	}
	// A manual write dedups against main only.
	if again, _ := m.Add(ctx, "chat", "preference", "", text, "user"); again != mainID {
		t.Errorf("manual duplicate = %d, want main row %d", again, mainID)
	}
	if k, _ := kindOf(aID); k != "decision" {
		t.Errorf("manual write changed thread row kind to %s", k)
	}
}

func TestExtractStoresThread(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.IngestExtracted(ctx, "chat", threadA, []ExtractedFact{
		{Kind: "fact", Content: "F one"},
		{Kind: "decision", Content: "D one"},
		{Kind: "preference", Content: "P one"},
		{Kind: "hypothesis", Content: "H one"},
	})
	rows, err := m.store.DB().QueryContext(ctx, `SELECT kind, thread_id FROM memories`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	n := 0
	for rows.Next() {
		var kind string
		var th int
		if err := rows.Scan(&kind, &th); err != nil {
			t.Fatal(err)
		}
		n++
		if th != threadA {
			t.Errorf("%s stored in thread %d, want %d", kind, th, threadA)
		}
	}
	if n != 4 {
		t.Errorf("stored %d rows, want 4", n)
	}
}

// A thread's supersedes_keyword only retires rows of the same
// conversation, never main's or a sibling's.
func TestSupersedeKeywordStaysInThread(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	mainID, _ := m.Add(ctx, "chat", "fact", "", "uses Golang daily", "user")
	bID, _ := m.AddInThread(ctx, "chat", threadB, "fact", "", "Golang for the bot", "extraction")
	aID, _ := m.AddInThread(ctx, "chat", threadA, "fact", "", "Golang for the CLI", "extraction")
	m.IngestExtracted(ctx, "chat", threadA, []ExtractedFact{
		{Kind: "fact", Content: "switched to Rust", SupersedesKeyword: "Golang"},
	})
	status := func(id int64) string {
		var s string
		m.store.DB().QueryRowContext(ctx, `SELECT IFNULL(status,'active') FROM memories WHERE id = ?`, id).Scan(&s)
		return s
	}
	if status(mainID) != "active" || status(bID) != "active" {
		t.Errorf("supersede crossed conversations: main=%s B=%s", status(mainID), status(bID))
	}
	if status(aID) != "superseded" {
		t.Errorf("own-thread row = %s, want superseded", status(aID))
	}
}

func TestSummaryRulesUnchanged(t *testing.T) {
	ctx := context.Background()
	m := newThreadTestMemory(t)
	m.AddInThread(ctx, "chat", 0, "conversation_summary", "", "otter MAINSUM", "compact")
	m.AddInThread(ctx, "chat", threadA, "conversation_summary", "", "otter ASUM", "compact")
	m.AddInThread(ctx, "chat", threadB, "conversation_summary", "", "otter BSUM", "compact")
	want := map[int]string{0: "MAINSUM", threadA: "ASUM", threadB: "BSUM"}
	for viewer, own := range want {
		// Non-empty query with an under-filled budget: the fallback
		// runs and its search matches every summary visible to viewer.
		got := m.CompileContext(ctx, "chat", viewer, "otter", DefaultBudget)
		for _, sum := range want {
			if n := strings.Count(got, sum); (sum == own && n != 1) || (sum != own && n != 0) {
				t.Errorf("thread %d: %s appears %d times; context %q", viewer, sum, n, got)
			}
		}
	}
}
