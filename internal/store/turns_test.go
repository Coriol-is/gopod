package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "store.sqlite"), silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func sampleTurn() Turn {
	return Turn{
		Source: "telegram", SourceID: "42", ChatFolder: "owner", ChatID: 100,
		TGMessageID: 42, IsOwner: true, Text: "hello", Status: TurnPending,
	}
}

func TestInsertTurnAssignsIDAndDefaults(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, dup, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil || dup || id == 0 {
		t.Fatalf("InsertTurn = (%d, %v, %v), want (>0, false, nil)", id, dup, err)
	}
	got, err := s.GetTurn(ctx, id)
	if err != nil {
		t.Fatalf("GetTurn: %v", err)
	}
	if got.Status != TurnPending || got.Attempts != 0 || got.CreatedAt == 0 {
		t.Errorf("row = %+v, want pending/0 attempts/created_at set", got)
	}
	if got.Text != "hello" || got.ChatFolder != "owner" || !got.IsOwner || got.TGMessageID != 42 {
		t.Errorf("row fields not round-tripped: %+v", got)
	}
}

func TestInsertTurnDuplicateIsNoop(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id1, _, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil {
		t.Fatal(err)
	}
	id2, dup, err := s.InsertTurn(ctx, sampleTurn())
	if err != nil {
		t.Fatal(err)
	}
	if !dup || id2 != 0 {
		t.Errorf("second insert = (%d, %v), want (0, true)", id2, dup)
	}
	rows, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].ID != id1 {
		t.Errorf("unfinished = %+v, want exactly the first row", rows)
	}
}

func TestInsertTurnRejectsEmptyKeys(t *testing.T) {
	s := openTestStore(t)
	tr := sampleTurn()
	tr.SourceID = ""
	if _, _, err := s.InsertTurn(context.Background(), tr); err == nil {
		t.Error("empty SourceID accepted")
	}
	tr = sampleTurn()
	tr.ChatFolder = ""
	if _, _, err := s.InsertTurn(context.Background(), tr); err == nil {
		t.Error("empty ChatFolder accepted")
	}
}

func TestMarkTurnRunningIncrementsAttempts(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	for i := 1; i <= 2; i++ {
		if err := s.MarkTurnRunning(ctx, id); err != nil {
			t.Fatal(err)
		}
		got, _ := s.GetTurn(ctx, id)
		if got.Status != TurnRunning || got.Attempts != i || got.StartedAt == 0 {
			t.Errorf("after MarkTurnRunning #%d: %+v", i, got)
		}
	}
}

func TestMarkTurnFinishedSetsStatusAndError(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	_ = s.MarkTurnRunning(ctx, id)
	if err := s.MarkTurnFinished(ctx, id, TurnFailed, "boom"); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTurn(ctx, id)
	if got.Status != TurnFailed || got.Error != "boom" || got.FinishedAt == 0 {
		t.Errorf("row = %+v", got)
	}
	if err := s.MarkTurnFinished(ctx, 9999, TurnDone, ""); !errors.Is(err, ErrTurnNotFound) {
		t.Errorf("unknown id err = %v, want ErrTurnNotFound", err)
	}
}

func TestSetTurnPlaceholderAndReply(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, _, _ := s.InsertTurn(ctx, sampleTurn())
	if err := s.SetTurnPlaceholder(ctx, id, 7); err != nil {
		t.Fatal(err)
	}
	if err := s.SetTurnReply(ctx, id, 8); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetTurn(ctx, id)
	if got.PlaceholderMsgID != 7 || got.ReplyMsgID != 8 {
		t.Errorf("memo = (%d, %d), want (7, 8)", got.PlaceholderMsgID, got.ReplyMsgID)
	}
}

func TestListUnfinishedTurnsFiltersAndOrders(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	mk := func(sid, status string) int64 {
		tr := sampleTurn()
		tr.SourceID = sid
		id, _, _ := s.InsertTurn(ctx, tr)
		if status != TurnPending {
			_ = s.MarkTurnRunning(ctx, id)
			if status != TurnRunning {
				_ = s.MarkTurnFinished(ctx, id, status, "")
			}
		}
		return id
	}
	a := mk("1", TurnRunning)
	mk("2", TurnDone)
	b := mk("3", TurnPending)
	mk("4", TurnFailed)
	c := mk("5", TurnInterrupted)
	rows, err := s.ListUnfinishedTurns(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 3 || rows[0].ID != a || rows[1].ID != b || rows[2].ID != c {
		t.Errorf("unfinished ids = %v, want [%d %d %d]", ids(rows), a, b, c)
	}
}

func ids(rows []Turn) []int64 {
	out := make([]int64, len(rows))
	for i, r := range rows {
		out[i] = r.ID
	}
	return out
}
