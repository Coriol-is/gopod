package queue

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

type reconciliationRequired struct{}

func (reconciliationRequired) Error() string   { return "frozen member" }
func (reconciliationRequired) HoldBatch() bool { return true }

type ineligibleBatchStore struct {
	*statusStore
	starts atomic.Int32
}

func (s *ineligibleBatchStore) StartBatch(context.Context, []Item) ([]int, error) {
	s.starts.Add(1)
	return nil, reconciliationRequired{}
}

func TestIneligibleMixedBatchParksWithoutCompletionOrOvertaking(t *testing.T) {
	st := &ineligibleBatchStore{statusStore: newStatusStore()}
	var calls, completions atomic.Int32
	q := New(t.Context(), func(context.Context, []Item) error { calls.Add(1); return nil }, 1, st, silentLog())
	defer q.Close()
	q.OnDone(func(Item, error) { completions.Add(1) })
	q.BeginDrain()
	a, _ := q.Enqueue(t.Context(), Item{Folder: "a", Source: "telegram", SourceID: "a"})
	b, _ := q.Enqueue(t.Context(), Item{Folder: "a", Source: "telegram", SourceID: "b"})
	q.CancelDrain()
	deadline := time.Now().Add(5 * time.Second)
	for q.OrdinaryHolds()["a"] == "" {
		if time.Now().After(deadline) {
			t.Fatal("no hold")
		}
		time.Sleep(time.Millisecond)
	}
	q.Enqueue(t.Context(), Item{Folder: "a", Source: "telegram", SourceID: "c"})
	q.BeginDrain()
	q.CancelDrain()
	if err := q.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
	if q.Pending() != 3 || calls.Load() != 0 || completions.Load() != 0 || st.starts.Load() != 1 {
		t.Fatalf("pending=%d calls=%d completion=%d starts=%d", q.Pending(), calls.Load(), completions.Load(), st.starts.Load())
	}
	if st.statusOf(a) != "pending" || st.statusOf(b) != "pending" {
		t.Fatal("unstarted rows were settled")
	}
}

func TestOrdinaryHoldSurvivesManagedHoldRelease(t *testing.T) {
	h := newHarness(t, 1, nil)
	it := h.ledger.admit(5, "alice", 0)
	h.ledger.set(it.AttemptID, "running", "held")
	h.store.setUnfinished(it, Item{ID: 9, Folder: "alice", Source: "telegram", RecoveryHold: "frozen result", Text: "frozen"})
	if _, err := h.q.Recover(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := h.q.ReleaseHold(t.Context(), it.AttemptID, ProofOperatorReconciled, "local exit confirmed"); err != nil {
		t.Fatal(err)
	}
	if !h.q.FolderHeld("alice") || h.q.OrdinaryHolds()["alice"] == "" || h.q.Pending() != 1 {
		t.Fatal("managed release removed result hold")
	}
	h.q.Enqueue(t.Context(), Item{Folder: "bob", Source: "telegram", Text: "free"})
	h.waitFor("unrelated folder", func() bool { return contains(h.orderList(), "i:free") })
	if h.nativeCalls() != 0 {
		t.Fatal("held native execution replayed")
	}
}
