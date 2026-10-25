package main

import (
	"context"
	"sync"
)

// shutdownHooks runs work between the shutdown signal and the
// cancellation of the app context, while every subsystem still runs
// (D027: the restart notice goes out before gopod stops accepting work).
type shutdownHooks struct {
	mu    sync.Mutex
	fired bool
	hooks []func()
}

// gracefulContext returns the app context: it is cancelled once sig is
// done and every hook registered with the returned func has run, in
// registration order. Hooks must bound their own duration. After the
// first signal stop is called, so a second SIGINT/SIGTERM kills the
// process at once. A hook registered after the signal never runs.
func gracefulContext(sig context.Context, stop context.CancelFunc) (context.Context, func(func())) {
	ctx, cancel := context.WithCancel(context.Background())
	h := &shutdownHooks{}
	go func() {
		<-sig.Done()
		stop()
		h.mu.Lock()
		h.fired = true
		hooks := h.hooks
		h.mu.Unlock()
		for _, fn := range hooks {
			fn()
		}
		cancel()
	}()
	register := func(fn func()) {
		h.mu.Lock()
		defer h.mu.Unlock()
		if !h.fired {
			h.hooks = append(h.hooks, fn)
		}
	}
	return ctx, register
}
