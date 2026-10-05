package memory

import (
	"crypto/rand"
	"encoding/hex"
	"sync"
	"time"
)

// tokenTTL bounds a capability's life in case the runner never revokes
// it (crash between exec start and the deferred Revoke). Normal life is
// one agent exec: minted before, revoked after.
const tokenTTL = time.Hour

// CapabilityEnv is the environment variable the agent reads the token
// from. The name deliberately avoids KEY/SECRET/TOKEN: Codex's default
// shell_environment_policy strips variables whose names contain those,
// and the agent's shell must see this one.
const CapabilityEnv = "GOPOD_MEMORY_CAPABILITY"

// Tokens maps per-exec capability tokens to chat folders. The memory
// API derives the chat from the token, never from request data, which
// keeps authorization path-based (CLAUDE.md) even though the API is
// reachable from every agent container over the Docker bridge.
//
// In-memory only: a token is worth exactly one agent exec and dies with
// the process.
type Tokens struct {
	mu      sync.Mutex
	byToken map[string]tokenEntry
	now     func() time.Time
}

type tokenEntry struct {
	chat    string
	expires time.Time
}

// NewTokens returns an empty token store.
func NewTokens() *Tokens {
	return &Tokens{byToken: make(map[string]tokenEntry), now: time.Now}
}

// Mint creates a fresh token bound to chat. Expired entries are swept
// on every mint so a missed Revoke cannot grow the map unbounded.
func (t *Tokens) Mint(chat string) string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("memory: crypto/rand unavailable: " + err.Error())
	}
	tok := hex.EncodeToString(b[:])
	now := t.now()
	t.mu.Lock()
	defer t.mu.Unlock()
	for k, e := range t.byToken {
		if now.After(e.expires) {
			delete(t.byToken, k)
		}
	}
	t.byToken[tok] = tokenEntry{chat: chat, expires: now.Add(tokenTTL)}
	return tok
}

// Lookup resolves a token to its chat folder. Expired tokens do not
// resolve even before the next sweep.
func (t *Tokens) Lookup(tok string) (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	e, ok := t.byToken[tok]
	if !ok || t.now().After(e.expires) {
		return "", false
	}
	return e.chat, true
}

// Revoke forgets a token. Safe to call twice.
func (t *Tokens) Revoke(tok string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.byToken, tok)
}
