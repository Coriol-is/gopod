package memory

import (
	"testing"
	"time"
)

func TestTokensMintLookupRevoke(t *testing.T) {
	ts := NewTokens()
	tok := ts.Mint("owner")
	if len(tok) != 64 {
		t.Fatalf("token length = %d, want 64 hex chars", len(tok))
	}
	if chat, ok := ts.Lookup(tok); !ok || chat != "owner" {
		t.Errorf("Lookup = (%q, %v), want (owner, true)", chat, ok)
	}
	if _, ok := ts.Lookup("nope"); ok {
		t.Error("unknown token resolved")
	}
	ts.Revoke(tok)
	if _, ok := ts.Lookup(tok); ok {
		t.Error("revoked token still resolves")
	}
	ts.Revoke(tok) // idempotent
}

func TestTokensAreUnique(t *testing.T) {
	ts := NewTokens()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		tok := ts.Mint("c")
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
	tok := ts.Mint("owner")
	ts.now = func() time.Time { return now.Add(tokenTTL + time.Second) }
	if _, ok := ts.Lookup(tok); ok {
		t.Error("token past TTL still resolves")
	}
	ts.Mint("other") // sweep runs on Mint
	ts.mu.Lock()
	n := len(ts.byToken)
	ts.mu.Unlock()
	if n != 1 {
		t.Errorf("expired token not swept, map has %d entries, want 1", n)
	}
}
