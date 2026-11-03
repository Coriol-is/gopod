package main

import (
	"context"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestProductionBatchAttemptsCanFreezeCoalescedResult(t *testing.T) {
	r, _, _ := restartFixture(t)
	conv, err := r.st.SnapshotConversation(t.Context(), "tg:1", 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	q := queue.New(t.Context(), func(ctx context.Context, items []queue.Item) error {
		if len(items) != 2 {
			t.Errorf("batch size %d", len(items))
		}
		var members []store.ResultMember
		for _, it := range items {
			tr, err := r.st.GetTurn(ctx, it.ID)
			if err != nil {
				return err
			}
			if it.Attempts != 1 || tr.Attempts != it.Attempts || tr.Status != store.TurnRunning {
				t.Errorf("uncommitted member %+v / %+v", it, tr)
			}
			members = append(members, store.ResultMember{TurnID: it.ID, Attempt: it.Attempts})
		}
		_, err := r.st.FreezeTurnResult(ctx, store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "claude", Members: members}, "one result")
		return err
	}, 1, turnStoreAdapter{st: r.st}, nil)
	defer q.Close()
	q.OnDone(func(_ queue.Item, err error) { done <- err })
	q.BeginDrain()
	a, _ := q.Enqueue(t.Context(), queue.Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "batch:1"})
	b, _ := q.Enqueue(t.Context(), queue.Item{Folder: "owner", ThreadID: 7, Source: "telegram", SourceID: "batch:2"})
	q.CancelDrain()
	for range 2 {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("batch did not complete")
		}
	}
	var resultID string
	for _, id := range []int64{a, b} {
		res, ok, err := r.st.TurnResultForAttempt(t.Context(), id, 1)
		if err != nil || !ok || len(res.Members) != 2 {
			t.Fatalf("no coalesced result: %+v %v %v", res, ok, err)
		}
		if resultID != "" && res.ID != resultID {
			t.Fatal("members have different results")
		}
		resultID = res.ID
	}
}
