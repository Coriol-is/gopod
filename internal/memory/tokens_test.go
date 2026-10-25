package memory

import (
	"testing"
	"time"
)

func TestTokensMintLookupRevoke(t *testing.T) {
	ts := NewTokens()
	tok := ts.Mint("owner", 7)
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}
	if chat, thread, ok := ts.Lookup(tok); !ok || chat != "owner" || thread != 7 {
		t.Errorf("Lookup = (%q, %d, %v), want (owner, 7, true)", chat, thread, ok)
	}
	if _, _, ok := ts.Lookup("nope"); ok {
		t.Error("unknown token resolved")
	}
	ts.Revoke(tok)
	if _, _, ok := ts.Lookup(tok); ok {
		t.Error("revoked token still resolves")
	}
	ts.Revoke(tok) // idempotent
}

func TestTokensAreUnique(t *testing.T) {
	ts := NewTokens()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := ts.Mint("c", 0)
		if seen[tok] {
			t.Fatal("duplicate token minted")
		}
		seen[tok] = true
	}
}

func TestTokensExpireAfterTTL(t *testing.T) {
	ts := NewTokens()
	now := time.Now()
	ts.now = func() time.Time { return now }
	tok := ts.Mint("owner", 0)
	ts.now = func() time.Time { return now.Add(tokenTTL + time.Second) }
	if _, _, ok := ts.Lookup(tok); ok {
		t.Error("token past TTL still resolves")
	}
	ts.Mint("other", 0) // sweep runs on Mint
	ts.mu.Lock()
	n := len(ts.byToken)
	ts.mu.Unlock()
	if n != 1 {
		t.Errorf("expired token not swept, map has %d entries, want 1", n)
	}
}

// Tokens of two conversations of one chat resolve to their own thread.
func TestTokensCarryThread(t *testing.T) {
	ts := NewTokens()
	a, b := ts.Mint("chat", 7), ts.Mint("chat", 9)
	if _, th, _ := ts.Lookup(a); th != 7 {
		t.Errorf("token A resolves to thread %d, want 7", th)
	}
	if _, th, _ := ts.Lookup(b); th != 9 {
		t.Errorf("token B resolves to thread %d, want 9", th)
	}
}
