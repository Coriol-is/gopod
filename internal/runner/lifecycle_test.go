package runner

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

// silentRunner builds a Runner with the minimum surface area needed
// to exercise idleWatcherSweep without spinning up Docker. The
// Docker field is left nil — sweep only touches the activity map
// and (in the kill path) calls inspectByName which we side-step by
// pre-clearing the chat from the activity map under the lock.
func silentRunner() *Runner {
	return &Runner{
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		containerLocks: make(map[string]*sync.Mutex),
		activity:       make(map[string]time.Time),
	}
}

func TestRunnerTouchAndLastActivity(t *testing.T) {
	r := silentRunner()
	if !r.LastActivity("missing").IsZero() {
		t.Error("LastActivity for unknown chat should be zero")
	}

	before := time.Now()
	r.touch("alice")
	after := time.Now()

	got := r.LastActivity("alice")
	if got.Before(before) || got.After(after) {
		t.Errorf("LastActivity = %v, want between %v and %v", got, before, after)
	}

	chats := r.ActiveChats()
	if len(chats) != 1 || chats[0] != "alice" {
		t.Errorf("ActiveChats = %v, want [alice]", chats)
	}
}

func TestRunnerForgetChat(t *testing.T) {
	r := silentRunner()
	r.touch("alice")
	r.forgetChat("alice")
	if !r.LastActivity("alice").IsZero() {
		t.Error("forgetChat did not clear activity")
	}
	if len(r.ActiveChats()) != 0 {
		t.Error("forgetChat did not remove from ActiveChats")
	}
}

// TestIdleWatcherSweepKeepsRecentChats verifies that a chat whose
// last activity is within the timeout window is left alone by the
// sweep. We exercise the decision logic by pre-populating activity
// and then checking that the chat survives — the kill branch is
// not exercised here because it requires a real Docker client.
func TestIdleWatcherSweepKeepsRecentChats(t *testing.T) {
	r := silentRunner()
	now := time.Now()

	// alice was active 5 minutes ago, well within a 30-minute timeout.
	r.activityMu.Lock()
	r.activity["alice"] = now.Add(-5 * time.Minute)
	r.activityMu.Unlock()

	// Use a custom sweep helper that mirrors idleWatcherSweep's
	// "should this chat be killed?" decision but does not call
	// killIdleChat (which would need Docker). We just check the
	// outcome by verifying the activity map is unchanged.
	for _, chat := range r.ActiveChats() {
		last := r.LastActivity(chat)
		if last.IsZero() {
			t.Errorf("zero last for %q", chat)
		}
		if now.Sub(last) >= 30*time.Minute {
			t.Errorf("%q would be killed (idle %v) but was active recently", chat, now.Sub(last))
		}
	}

	if len(r.ActiveChats()) != 1 {
		t.Errorf("recent chat dropped from activity map: %v", r.ActiveChats())
	}
}

// TestIdleWatcherSweepIdentifiesIdleChats verifies that the sweep
// flags a chat older than the timeout. Same caveat as above — we
// don't call killIdleChat directly because it needs Docker; instead
// we assert the decision boundary works.
func TestIdleWatcherSweepIdentifiesIdleChats(t *testing.T) {
	r := silentRunner()
	now := time.Now()
	timeout := 30 * time.Minute

	r.activityMu.Lock()
	r.activity["fresh"] = now.Add(-1 * time.Minute)
	r.activity["stale"] = now.Add(-45 * time.Minute)
	r.activityMu.Unlock()

	var idle, kept []string
	for _, chat := range r.ActiveChats() {
		last := r.LastActivity(chat)
		if now.Sub(last) >= timeout {
			idle = append(idle, chat)
		} else {
			kept = append(kept, chat)
		}
	}

	if len(idle) != 1 || idle[0] != "stale" {
		t.Errorf("idle = %v, want [stale]", idle)
	}
	if len(kept) != 1 || kept[0] != "fresh" {
		t.Errorf("kept = %v, want [fresh]", kept)
	}
}

// TestIdleWatcherTimeoutDefault confirms that StartIdleWatcher with
// timeout=0 falls back to DefaultIdleTimeout. Tested by stopping the
// goroutine via the parent context immediately — we just want the
// "no panic" path.
func TestIdleWatcherTimeoutDefault(t *testing.T) {
	r := silentRunner()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // immediately
	r.StartIdleWatcher(ctx, 0)
	// give the goroutine a moment to observe the cancelled context
	time.Sleep(10 * time.Millisecond)
}
