//go:build docker_integration

package runner

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestIntegrationLifecycleBusyTTLAndForceDrain(t *testing.T) {
	d := mkDocker(t)
	name := "gopod-inttest-lifecycle-busy"
	id := startShContainer(t, d, name)
	r := integrationStreamRunner(d)
	stream, err := r.startStream(t.Context(), id, convKey{name, 0}, TierOwner, nil, noRestoreProvider{NewClaudeProvider("unused")}, "p", fixedCmd([]string{"sh", "-c", "setsid sleep 300 >/dev/null 2>&1 & echo ready; sleep 300"}))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-stream.Chunks:
	case <-time.After(10 * time.Second):
		t.Fatal("agent not started")
	}
	go func() {
		for range stream.Chunks {
		}
	}()
	r.activityMu.Lock()
	r.activity[name] = time.Now().Add(-time.Hour)
	r.activityMu.Unlock()
	r.idleWatcherSweep(t.Context(), time.Now(), time.Nanosecond)
	if d.containerDead(t.Context(), id) {
		t.Fatal("busy runtime evicted")
	}
	r.BeginDrain()
	wait, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if err := r.WaitDrained(wait); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("graceful drain interrupted run: %v", err)
	}
	if d.containerDead(t.Context(), id) {
		t.Fatal("drain timeout killed runtime")
	}
	force, stop := context.WithTimeout(t.Context(), 30*time.Second)
	defer stop()
	if err := r.ForceDrain(force); err != nil {
		t.Fatal(err)
	}
	if !d.containerDead(force, id) {
		t.Fatal("force did not establish container death")
	}
	if res := <-stream.Result; !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("result=%v", res.Err)
	}
}

func TestIntegrationLifecycleStartupQuarantine(t *testing.T) {
	d := mkDocker(t)
	name := "gopod-inttest-lifecycle-leftover"
	id := startShContainer(t, d, name)
	if _, err := d.CleanupLeftovers(t.Context(), "new-version"); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatal(err)
	}
	if d.containerDead(t.Context(), id) {
		t.Fatal("startup cleanup killed running leftover")
	}
	r := integrationStreamRunner(d)
	if _, err := r.acquireAgentSlot(t.Context(), name); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatalf("quarantined admission: %v", err)
	}
	if err := d.Stop(t.Context(), id, time.Second); err != nil {
		t.Fatal(err)
	}
	if r.ExecutionUncertain(t.Context(), name) {
		t.Fatal("proven stopped container stayed quarantined")
	}
	release, err := r.acquireAgentSlot(t.Context(), name)
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestIntegrationLifecycleDetachedProcessBlocksDrain(t *testing.T) {
	d := mkDocker(t)
	folder := "inttest-lifecycle-detached"
	id := startShContainer(t, d, ContainerName(folder))
	r := integrationStreamRunner(d)
	release, err := r.acquireAgentSlot(t.Context(), folder)
	if err != nil {
		t.Fatal(err)
	}
	res, err := d.Exec(t.Context(), id, []string{"sh", "-c", "setsid sleep 300 >/dev/null 2>&1 &"}, nil)
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("spawn detached: %v %+v", err, res)
	}
	release()
	r.BeginDrain()
	if err := r.WaitDrained(t.Context()); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatalf("detached writer not held: %v", err)
	}
	if d.containerDead(t.Context(), id) {
		t.Fatal("ordinary drain killed detached process")
	}
	if err := d.Stop(t.Context(), id, time.Second); err != nil {
		t.Fatal(err)
	}
	if err := r.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
}
