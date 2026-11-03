package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDrainKeepsBusySilentRuntime(t *testing.T) {
	r := silentRunner()
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	r.activity["owner"] = time.Now().Add(-time.Hour)
	// Calling the actual sweep would dereference nil Docker if it chose to kill.
	r.idleWatcherSweep(t.Context(), time.Now(), time.Nanosecond)
	r.BeginDrain()
	if _, err := r.acquireAgentSlot(t.Context(), "other"); !errors.Is(err, ErrDraining) {
		t.Fatalf("admission=%v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.WaitDrained(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("busy drain=%v", err)
	}
	if r.LastActivity("owner").IsZero() {
		t.Fatal("busy runtime forgotten")
	}
	release()
	if err := r.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
	r.CancelDrain()
	next, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	next()
}

func TestIdleClaimExcludesNewAgentWithoutCancellingBackground(t *testing.T) {
	r := silentRunner()
	idle, ok := r.tryIdleSlot("owner")
	if !ok {
		t.Fatal("idle claim failed")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.acquireAgentSlot(ctx, "owner"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	idle()
	ctx, cancel = context.WithCancel(t.Context())
	defer cancel()
	release, err := r.acquireBackgroundSlot(ctx, "owner", cancel)
	if err != nil {
		t.Fatal(err)
	}
	if idle, ok := r.tryIdleSlot("owner"); ok {
		idle()
		t.Fatal("idle claimed maintenance")
	}
	if ctx.Err() != nil {
		t.Fatal("idle check cancelled maintenance")
	}
	release()
}

func TestForceDrainCancelsExistingAndRacingOperation(t *testing.T) {
	r := silentRunner()
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	op, finish := r.operationContext(t.Context())
	r.CancelActive()
	if !errors.Is(op.Err(), context.Canceled) {
		t.Fatal("active operation not cancelled")
	}
	late, finishLate := r.operationContext(t.Context())
	if !errors.Is(late.Err(), context.Canceled) {
		t.Fatal("racing registration not cancelled")
	}
	finishLate()
	finish()
	release()
	if err := r.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUnprovenCleanupBlocksIdleAdmissionAndDrain(t *testing.T) {
	r := silentRunner()
	release, err := r.acquireAgentSlot(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	r.markExecutionUnknown("owner", "container")
	release()
	if idle, ok := r.tryIdleSlot("owner"); ok {
		idle()
		t.Fatal("unknown runtime is not idle")
	}
	if _, err := r.acquireAgentSlot(t.Context(), "owner"); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatal(err)
	}
	if err := r.WaitDrained(t.Context()); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatal(err)
	}
}

func TestWaitDrainedIncludesAuxiliaryOperation(t *testing.T) {
	r := silentRunner()
	_, finish := r.operationContext(t.Context())
	r.BeginDrain()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := r.WaitDrained(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("auxiliary ignored: %v", err)
	}
	finish()
	if err := r.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownExecutionRetainsGlobalCapacity(t *testing.T) {
	r := silentRunner()
	r.gate = newGate(1, 0, nil, r.log)
	release, err := r.admit(t.Context(), "owner", ClassInteractive)
	if err != nil {
		t.Fatal(err)
	}
	r.markExecutionUnknown("owner", "container")
	r.releaseGateWhenSettled(t.Context(), "owner", release)
	running, _ := r.gate.counts()
	if running != 1 {
		t.Fatalf("unknown capacity released: %d", running)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	waiting := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		_, err := r.gate.acquire(ctx, ClassInteractive, func(WaitReason) { close(waiting) })
		result <- err
	}()
	select {
	case <-waiting:
	case err := <-result:
		t.Fatalf("other namespace admitted: %v", err)
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
