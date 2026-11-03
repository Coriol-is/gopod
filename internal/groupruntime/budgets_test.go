package groupruntime

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestTurnBudgetPersistsAndDoesNotCountValidation(t *testing.T) {
	m, c := admissionFixture(t)
	m.Config.MaxTurnsPerGroupDay = 1
	now := time.Now()
	release, err := m.reserveTurn(t.Context(), c.NamespaceID, now)
	if err != nil {
		t.Fatal(err)
	}
	for range 5 {
		if err := m.Authorize(t.Context(), c); err != nil {
			t.Fatalf("last admitted turn revoked: %v", err)
		}
	}
	release()
	release()
	if m.Authorize(t.Context(), c) == nil {
		t.Fatal("exhausted budget admitted")
	}
	restarted := &Manager{Store: m.Store, Config: m.Config}
	if _, err := restarted.reserveTurn(t.Context(), c.NamespaceID, now); err == nil {
		t.Fatal("restart reset budget")
	}
	if _, err := restarted.reserveTurn(t.Context(), c.NamespaceID, now.Add(-24*time.Hour)); err == nil {
		t.Fatal("clock rollback reset budget")
	}
	release, err = restarted.reserveTurn(t.Context(), c.NamespaceID, now.Add(24*time.Hour))
	if err != nil {
		t.Fatal("new UTC day not admitted", err)
	}
	release()
}
func TestConcurrentGroupReservationAtomic(t *testing.T) {
	m, _ := admissionFixture(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	var mu sync.Mutex
	var releases []func()
	for _, namespace := range []string{"one", "two", "three", "four"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			release, err := m.reserveTurn(ctx, namespace, time.Now())
			if err == nil {
				mu.Lock()
				releases = append(releases, release)
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if len(releases) != 1 {
		t.Fatalf("admitted %d groups concurrently", len(releases))
	}
	releases[0]()
	release, err := m.reserveTurn(ctx, "next", time.Now())
	if err != nil {
		t.Fatal("capacity not released", err)
	}
	release()
}
func TestTurnBudgetCorruptionFailsClosed(t *testing.T) {
	m, c := admissionFixture(t)
	for _, raw := range []string{"broken", `{"Day":"2026-10-31","Turns":-1}`, `{"Turns":0}`} {
		if err := m.Store.SetState(t.Context(), "group_turn_budget:"+c.NamespaceID, raw); err != nil {
			t.Fatal(err)
		}
		if _, err := m.reserveTurn(t.Context(), c.NamespaceID, time.Now()); err == nil {
			t.Fatal("corrupt budget admitted", raw)
		}
	}
}
