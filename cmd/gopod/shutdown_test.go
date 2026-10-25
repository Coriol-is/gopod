package main

import (
	"context"
	"testing"
	"time"
)

func TestGracefulContextRunsHooksBeforeCancel(t *testing.T) {
	sig, fire := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	ctx, onShutdown := gracefulContext(sig, func() { close(stopped) })

	var order []string
	onShutdown(func() {
		if ctx.Err() != nil {
			t.Error("app context cancelled before the hook ran")
		}
		order = append(order, "a")
	})
	onShutdown(func() { order = append(order, "b") })

	select {
	case <-ctx.Done():
		t.Fatal("app context cancelled before the signal")
	case <-time.After(20 * time.Millisecond):
	}
	fire()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("app context not cancelled after the signal")
	}
	<-stopped
	if len(order) != 2 || order[0] != "a" || order[1] != "b" {
		t.Fatalf("hooks ran %v, want [a b]", order)
	}
	ran := false
	onShutdown(func() { ran = true })
	time.Sleep(10 * time.Millisecond)
	if ran {
		t.Error("hook registered after the signal ran")
	}
}
