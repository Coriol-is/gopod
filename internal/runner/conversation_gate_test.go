package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestConversationGateWaitsAndRejectsStaleReopen(t *testing.T) {
	var gate ConversationGate
	active, release, err := gate.Enter(t.Context(), "owner", 7)
	if err != nil {
		t.Fatal(err)
	}
	sibling, releaseSibling, err := gate.Enter(t.Context(), "owner", 8)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseSibling()
	old := gate.Block("owner", 7)
	if active.Err() == nil || sibling.Err() != nil {
		t.Fatal("cancellation crossed conversation boundary")
	}
	if _, _, err = gate.Enter(t.Context(), "owner", 7); !errors.Is(err, ErrConversationQuiescing) {
		t.Fatalf("admitted closed lane: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err = gate.Wait(ctx, "owner", 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("returned before lease settled: %v", err)
	}
	release()
	release()
	if err = gate.Wait(t.Context(), "owner", 7); err != nil {
		t.Fatal(err)
	}
	newer := gate.Block("owner", 7)
	if err = gate.Reopen("owner", 7, old); err == nil {
		t.Fatal("stale refusal reopened newer fence")
	}
	if err = gate.Reopen("owner", 7, newer); err != nil {
		t.Fatal(err)
	}
	_, done, err := gate.Enter(t.Context(), "owner", 7)
	if err != nil {
		t.Fatal(err)
	}
	done()
	gate.mu.Lock()
	n := len(gate.lanes)
	gate.mu.Unlock()
	if n != 1 {
		t.Fatalf("idle unblocked lanes retained: %d", n)
	}
}

func TestConversationSealDoesNotCancelIssuedTransport(t *testing.T) {
	var gate ConversationGate
	ctx, release, _ := gate.Enter(t.Context(), "chat", 7)
	gate.Seal("chat", 7)
	if ctx.Err() != nil {
		t.Fatal("sealing turned issued work into cancellation")
	}
	release()
	if err := gate.Wait(t.Context(), "chat", 7); err != nil {
		t.Fatal(err)
	}
}

func TestTopicNativeQuiescenceWaitsForActualStreamExit(t *testing.T) {
	r, fx, _ := newStopHarness(t, NewClaudeProvider("img"))
	fx.killGate = make(chan struct{})
	stream, err := r.RunStream(t.Context(), convChat, 7, TierRegistered, nil, "first")
	if err != nil {
		t.Fatal(err)
	}
	<-fx.started
	go func() {
		for range stream.Chunks {
		}
		for range stream.Result {
		}
	}()
	token := r.BeginConversationQuiescence(convChat, 7)
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err = r.WaitConversationQuiescent(ctx, convChat, 7); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("native kill still pending but wait returned %v", err)
	}
	if _, err = r.RunStream(t.Context(), convChat, 7, TierRegistered, nil, "blocked"); !errors.Is(err, ErrConversationQuiescing) {
		t.Fatalf("new native admission: %v", err)
	}
	close(fx.killGate)
	ctx2, cancel2 := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel2()
	if err = r.WaitConversationQuiescent(ctx2, convChat, 7); err != nil {
		t.Fatal(err)
	}
	if err = r.ReopenConversation(convChat, 7, token); err != nil {
		t.Fatal(err)
	}
}

func TestTopicNativeUnknownRemainsBlockedAfterLeaseExit(t *testing.T) {
	r := &Runner{}
	r.BeginConversationQuiescence("owner", 7)
	r.markExecutionUnknown("owner", "unproven")
	if err := r.WaitConversationQuiescent(t.Context(), "owner", 7); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatalf("unknown execution lost: %v", err)
	}
}
