package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestProductionFrozenRecoveryHoldsFolderUntilPublication(t *testing.T) {
	r, _, _ := restartFixture(t)
	ctx := t.Context()
	conv, err := r.st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	id, _, err := r.st.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: "frozen", ChatFolder: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.st.MarkTurnRunning(ctx, id); err != nil {
		t.Fatal(err)
	}
	result, err := r.st.FreezeTurnResult(ctx, store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "codex", Members: []store.ResultMember{{TurnID: id, Attempt: 1}}}, "frozen answer")
	if err != nil {
		t.Fatal(err)
	}
	// Interrupted rows must remain visible to backup idle, too.
	if err = r.st.MarkTurnFinished(ctx, id, store.TurnInterrupted, ""); err != nil {
		t.Fatal(err)
	}
	var ownerCalls atomic.Int32
	for restart := 0; restart < 2; restart++ {
		otherDone := make(chan struct{}, 1)
		q := queue.New(ctx, func(_ context.Context, items []queue.Item) error {
			if items[0].Folder == "owner" {
				ownerCalls.Add(1)
			} else {
				otherDone <- struct{}{}
			}
			return nil
		}, 1, turnStoreAdapter{st: r.st}, nil)
		if _, err := q.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if q.OrdinaryHolds()["owner"] == "" || !q.FolderHeld("owner") {
			t.Fatal("missing visible hold")
		}
		if restart == 0 {
			q.Enqueue(ctx, queue.Item{Folder: "owner", Source: "telegram", SourceID: "newer"})
			q.Enqueue(ctx, queue.Item{Folder: "other", Source: "telegram", SourceID: "unrelated"})
			select {
			case <-otherDone:
			case <-time.After(5 * time.Second):
				t.Fatal("hold consumed global capacity")
			}
		}
		if q.Pending() != 2 {
			t.Fatalf("held work hidden: pending=%d", q.Pending())
		}
		q.BeginDrain()
		q.CancelDrain()
		q.Close()
		if ownerCalls.Load() != 0 {
			t.Fatal("native replay or overtaking")
		}
		original, _ := r.st.GetTurn(ctx, id)
		if original.Status != store.TurnInterrupted || original.Attempts != 1 {
			t.Fatal(original)
		}
	}
	// A reviewed publisher takes ownership using the exact stored result. Boot
	// then resumes later input, never invokes the model for the frozen answer.
	delivery, err := r.st.PublishDelivery(ctx, store.PublishRequest{Conversation: conv, ResultID: result.ID, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{{Kind: store.PartText, Payload: []byte("frozen answer")}}})
	if err != nil {
		t.Fatal(err)
	}
	q := queue.New(ctx, func(_ context.Context, items []queue.Item) error {
		for _, it := range items {
			if it.ID == id {
				t.Error("frozen model replay")
			}
		}
		ownerCalls.Add(1)
		return nil
	}, 1, turnStoreAdapter{st: r.st}, nil)
	done := make(chan struct{}, 1)
	q.OnDone(func(queue.Item, error) { done <- struct{}{} })
	if n, err := q.Recover(ctx); err != nil || n != 1 {
		t.Fatal(n, err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("later input not resumed")
	}
	q.Close()
	if ownerCalls.Load() != 1 {
		t.Fatal(ownerCalls.Load())
	}
	got, err := r.st.GetDelivery(ctx, delivery.ID)
	if err != nil || got.State != store.DeliveryPending || got.Parts[0].Attempts != 0 {
		t.Fatal(got, err)
	}
}
