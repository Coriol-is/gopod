package telegram

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type capabilityClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *capabilityClock) read() time.Time     { c.mu.Lock(); defer c.mu.Unlock(); return c.now }
func (c *capabilityClock) add(d time.Duration) { c.mu.Lock(); c.now = c.now.Add(d); c.mu.Unlock() }
func waitCapability(t *testing.T, c *capabilityCache) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	c.Refresh(ctx)
	if c.Snapshot().RefreshState == "in_flight" {
		t.Fatal("refresh did not finish")
	}
}

func TestCapabilityCacheFreshStaleFailureRecovery(t *testing.T) {
	clock := &capabilityClock{now: time.Unix(1000, 0)}
	var calls atomic.Int32
	c := newCapabilityCache(func(context.Context) capabilityResult {
		switch calls.Add(1) {
		case 2:
			return capabilityResult{class: "transport"}
		default:
			return capabilityResult{valid: true, username: "test_bot", settings: map[string]string{"supports_inline_queries": "enabled"}}
		}
	}, clock.read)
	if got := c.Snapshot(); got.Freshness != "unknown" || got.Settings["supports_inline_queries"] != "unknown" {
		t.Fatal(got)
	}
	c.Start(t.Context())
	defer c.Close()
	waitCapability(t, c)
	first := c.Snapshot()
	if first.Freshness != "fresh" || first.RefreshState != "cooldown" || first.Username != "test_bot" {
		t.Fatal(first)
	}
	first.Settings["supports_inline_queries"] = "disabled"
	if c.Snapshot().Settings["supports_inline_queries"] != "enabled" {
		t.Fatal("snapshot mutation leaked")
	}
	c.Refresh(t.Context())
	if calls.Load() != 1 {
		t.Fatal("cooldown bypass")
	}
	clock.add(capabilityTTL)
	if c.Snapshot().Freshness != "stale" {
		t.Fatal("TTL did not expire")
	}
	waitCapability(t, c)
	failed := c.Snapshot()
	if failed.Freshness != "stale" || failed.RefreshState != "backoff" || failed.Username != "test_bot" || !failed.CheckedAt.Equal(first.CheckedAt) || failed.Settings["supports_inline_queries"] != "enabled" {
		t.Fatal(failed)
	}
	clock.add(capabilityCooldown)
	waitCapability(t, c)
	if got := c.Snapshot(); got.Freshness != "fresh" || got.ErrorClass != "" || calls.Load() != 3 {
		t.Fatal(got, calls.Load())
	}
}

func TestCapabilityCacheSingleFlightAndWaiterCancellation(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	c := newCapabilityCache(func(ctx context.Context) capabilityResult {
		calls.Add(1)
		close(entered)
		select {
		case <-release:
			return capabilityResult{valid: true, username: "test_bot"}
		case <-ctx.Done():
			return capabilityResult{class: "canceled"}
		}
	}, nil)
	c.Start(t.Context())
	defer c.Close()
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if got := c.Refresh(ctx); got.RefreshState != "in_flight" {
		t.Fatal(got)
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() { c.Refresh(t.Context()) })
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 || c.Snapshot().Username != "test_bot" {
		t.Fatal("fetch canceled or duplicated", calls.Load())
	}
}

func TestCapabilityCacheShutdownJoins(t *testing.T) {
	entered := make(chan struct{})
	exited := make(chan struct{})
	c := newCapabilityCache(func(ctx context.Context) capabilityResult {
		close(entered)
		<-ctx.Done()
		close(exited)
		return capabilityResult{class: "canceled"}
	}, nil)
	c.Start(t.Context())
	<-entered
	c.Close()
	select {
	case <-exited:
	default:
		t.Fatal("Close did not join")
	}
	if c.Snapshot().RefreshState != "stopped" {
		t.Fatal(c.Snapshot())
	}
	c.Refresh(t.Context())
	c.Start(t.Context())
}

func TestCapabilityCacheBackoffBoundaries(t *testing.T) {
	clock := &capabilityClock{now: time.Unix(1000, 0)}
	c := newCapabilityCache(func(context.Context) capabilityResult {
		return capabilityResult{class: "rate_limit", retryAfter: time.Minute}
	}, clock.read)
	c.Start(t.Context())
	defer c.Close()
	waitCapability(t, c)
	for _, want := range []time.Duration{time.Minute, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		got := c.Snapshot()
		if d := got.NextRefresh.Sub(clock.read()); d != want {
			t.Fatalf("delay=%v want=%v", d, want)
		}
		clock.add(want)
		waitCapability(t, c)
	}
}

func TestCapabilityCacheRequestDeadline(t *testing.T) {
	c := newCapabilityCache(func(ctx context.Context) capabilityResult {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > capabilityDeadline || time.Until(deadline) < capabilityDeadline-time.Second {
			t.Error("missing five second fetch bound")
		}
		return capabilityResult{valid: true, username: "test_bot"}
	}, nil)
	c.Start(t.Context())
	defer c.Close()
	waitCapability(t, c)
}
