package telegram

import (
	"context"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
)

const (
	capabilityDeadline   = 5 * time.Second
	capabilityTTL        = 15 * time.Minute
	capabilityCooldown   = 30 * time.Second
	capabilityMaxBackoff = 5 * time.Minute
)

// capabilityCache owns fetch cancellation. A command waiter never owns it.
type capabilityCache struct {
	mu       sync.Mutex
	fetch    capabilityFetcher
	now      func() time.Time
	ctx      context.Context
	cancel   context.CancelFunc
	stopped  bool
	done     chan struct{}
	view     control.TelegramSnapshot
	failures uint
}

func newCapabilityCache(fetch capabilityFetcher, now func() time.Time) *capabilityCache {
	if now == nil {
		now = time.Now
	}
	return &capabilityCache{fetch: fetch, now: now}
}

// Start is idempotent and starts a single bounded asynchronous observation.
func (c *capabilityCache) Start(ctx context.Context) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ctx != nil || c.stopped {
		return
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.startLocked()
}

func (c *capabilityCache) Close() {
	c.mu.Lock()
	c.stopped = true
	if c.cancel != nil {
		c.cancel()
	}
	done := c.done
	c.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (c *capabilityCache) Snapshot() control.TelegramSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.snapshotLocked()
}

func (c *capabilityCache) snapshotLocked() control.TelegramSnapshot {
	view := c.view
	view.Settings = make(map[string]string, len(capabilityFields))
	for _, name := range capabilityFields {
		value := c.view.Settings[name]
		if value == "" {
			value = "unknown"
		}
		view.Settings[name] = value
	}
	view.AllowedUpdates = append([]string(nil), c.view.AllowedUpdates...)
	view.Freshness = "unknown"
	if !view.CheckedAt.IsZero() {
		view.Freshness = "fresh"
		if c.now().Sub(view.CheckedAt) >= capabilityTTL || view.ErrorClass != "" {
			view.Freshness = "stale"
		}
	}
	view.RefreshState = "idle"
	switch {
	case c.stopped || c.ctx == nil || c.ctx.Err() != nil:
		view.RefreshState = "stopped"
	case c.done != nil:
		view.RefreshState = "in_flight"
	case c.now().Before(view.NextRefresh):
		view.RefreshState = "cooldown"
		if c.failures != 0 {
			view.RefreshState = "backoff"
		}
	}
	return view
}

func (c *capabilityCache) Refresh(ctx context.Context) control.TelegramSnapshot {
	c.mu.Lock()
	if c.ctx == nil || c.stopped || c.ctx.Err() != nil {
		view := c.snapshotLocked()
		c.mu.Unlock()
		return view
	}
	if c.done == nil && !c.now().Before(c.view.NextRefresh) {
		c.startLocked()
	}
	done := c.done
	if done == nil {
		view := c.snapshotLocked()
		c.mu.Unlock()
		return view
	}
	c.mu.Unlock()
	select {
	case <-done:
	case <-ctx.Done():
	}
	return c.Snapshot()
}

func (c *capabilityCache) startLocked() {
	c.view.LastAttempt = c.now()
	done := make(chan struct{})
	c.done = done
	go func() {
		ctx, cancel := context.WithTimeout(c.ctx, capabilityDeadline)
		result := c.fetch(ctx)
		cancel()
		c.mu.Lock()
		defer c.mu.Unlock()
		now := c.now()
		c.view.ErrorClass = result.class
		if result.valid {
			c.view.Settings = result.settings
			c.view.Username = result.username
			c.view.CheckedAt = now
		}
		delay := capabilityCooldown
		if result.class == "" {
			c.failures = 0
		} else {
			if c.failures < 5 {
				c.failures++
			}
			delay = capabilityCooldown * time.Duration(1<<(c.failures-1))
			if delay > capabilityMaxBackoff {
				delay = capabilityMaxBackoff
			}
			if result.retryAfter > delay {
				delay = result.retryAfter
			}
			if delay > capabilityMaxBackoff {
				delay = capabilityMaxBackoff
			}
		}
		c.view.NextRefresh = now.Add(delay)
		c.done = nil
		close(done)
	}()
}
