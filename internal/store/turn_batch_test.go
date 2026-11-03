package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
)

func queuedBatch(t *testing.T, s *Store, thread int) []TurnStart {
	t.Helper()
	var batch []TurnStart
	for i := 0; i < 2; i++ {
		tr := sampleTurn()
		tr.ChatFolder = "owner"
		tr.ThreadID = thread
		tr.SourceID = fmt.Sprintf("batch:%d:%d", thread, i)
		id, _, err := s.InsertTurn(t.Context(), tr)
		if err != nil {
			t.Fatal(err)
		}
		batch = append(batch, TurnStart{ID: id, Folder: tr.ChatFolder, ThreadID: thread, Source: tr.Source})
	}
	return batch
}

func TestStartTurnBatchAtomicAttempts(t *testing.T) {
	s := openTestStore(t)
	batch := queuedBatch(t, s, 7)
	for n := 1; n <= 2; n++ {
		got, err := s.StartTurnBatch(t.Context(), batch)
		if err != nil {
			t.Fatal(err)
		}
		for i, it := range batch {
			tr, err := s.GetTurn(t.Context(), it.ID)
			if err != nil || got[i] != n || tr.Attempts != n || tr.Status != TurnRunning || tr.StartedAt == 0 {
				t.Fatalf("attempt %d: %+v / %v / %v", n, tr, got, err)
			}
			batch[i].Attempts = n
		}
	}
}

func TestStartTurnBatchRollsBackInvalidMember(t *testing.T) {
	for _, kind := range []string{"missing", "stale attempt", "wrong folder", "wrong thread", "wrong source", "duplicate", "write failure"} {
		t.Run(kind, func(t *testing.T) {
			s := openTestStore(t)
			batch := queuedBatch(t, s, 7)
			original := append([]TurnStart(nil), batch...)
			switch kind {
			case "missing":
				batch[1].ID = 99999
			case "stale attempt":
				batch[1].Attempts = 1
			case "wrong folder":
				batch[1].Folder = "other"
			case "wrong thread":
				batch[1].ThreadID = 8
			case "wrong source":
				batch[1].Source = "task"
			case "duplicate":
				batch[1] = batch[0]
			case "write failure":
				_, err := s.db.ExecContext(t.Context(), fmt.Sprintf(`CREATE TRIGGER reject_second BEFORE UPDATE ON turns WHEN NEW.id=%d BEGIN SELECT RAISE(ABORT,'injected batch failure'); END`, batch[1].ID))
				if err != nil {
					t.Fatal(err)
				}
			}
			if got, err := s.StartTurnBatch(t.Context(), batch); err == nil || got != nil {
				t.Fatalf("accepted %v: %v", got, err)
			}
			for _, it := range original {
				tr, err := s.GetTurn(t.Context(), it.ID)
				if err != nil {
					t.Fatal(err)
				}
				want := TurnPending
				if tr.Attempts != 0 || tr.Status != want {
					t.Fatalf("partial start: %+v", tr)
				}
			}
		})
	}
}

func TestStartTurnBatchConcurrentSnapshot(t *testing.T) {
	s := openTestStore(t)
	batch := queuedBatch(t, s, 0)
	start := make(chan struct{})
	outcomes := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, err := s.StartTurnBatch(t.Context(), batch); outcomes <- err }()
	}
	close(start)
	wg.Wait()
	close(outcomes)
	successes := 0
	for err := range outcomes {
		if err == nil {
			successes++
		} else if !errors.Is(err, ErrTurnBatchConflict) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("same snapshot dispatched %d times", successes)
	}
}

func TestStartTurnBatchRejectsManagedLedgerMember(t *testing.T) {
	f := newDeliveryFixture(t)
	_, a := admitted(t, f)
	tr, err := f.st.GetTurn(t.Context(), a.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.st.StartTurnBatch(t.Context(), []TurnStart{{ID: tr.ID, Folder: tr.ChatFolder, ThreadID: tr.ThreadID, Source: tr.Source, Attempts: tr.Attempts}})
	if !errors.Is(err, ErrTurnBatchConflict) {
		t.Fatalf("managed ledger member accepted: %v", err)
	}
	got, err := f.st.GetTurn(t.Context(), tr.ID)
	if err != nil || got.Attempts != tr.Attempts || got.Status != tr.Status {
		t.Fatalf("managed turn mutated %+v %v", got, err)
	}
}
