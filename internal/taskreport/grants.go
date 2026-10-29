package taskreport

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// MaxGrantTTL bounds a capability: the longest approved task lifetime plus a
// margin. A grant is also revoked explicitly at settlement or cancellation.
const MaxGrantTTL = 65 * time.Minute

// Binding is everything a capability stands for. No field of a report can
// widen it.
type Binding struct {
	Folder    string // IPC namespace the report must come from
	RunID     string
	AttemptID string
	Scope     store.ScopeRef // frozen at issue
}

// Grants is the in-memory registry of live report capabilities. A restart
// revokes them all: an agent whose process survived cannot report afterwards.
type Grants struct {
	now        func() time.Time
	validScope func(store.ScopeRef) error // optional; nil skips the scope check

	mu     sync.Mutex
	byHash map[[32]byte]*grant
}

type grant struct {
	b       Binding
	expires time.Time
	revoked bool
}

// NewGrants returns an empty registry. A nil now uses time.Now. validScope, when
// set, revalidates the frozen scope at every admission (store.ValidateScope).
func NewGrants(now func() time.Time, validScope func(store.ScopeRef) error) *Grants {
	if now == nil {
		now = time.Now
	}
	return &Grants{now: now, validScope: validScope, byHash: map[[32]byte]*grant{}}
}

// Issue mints an opaque token for one attempt. ttl is capped at MaxGrantTTL.
func (g *Grants) Issue(b Binding, ttl time.Duration) (string, error) {
	if b.Folder == "" || !validID(b.RunID) || !validID(b.AttemptID) || ttl <= 0 {
		return "", fmt.Errorf("taskreport: invalid binding")
	}
	ttl = min(ttl, MaxGrantTTL)
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("taskreport: token: %w", err)
	}
	tok := hex.EncodeToString(raw[:])
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	for k, st := range g.byHash { // prune
		if st.revoked || !st.expires.After(now) {
			delete(g.byHash, k)
		}
	}
	g.byHash[sha256.Sum256([]byte(tok))] = &grant{b: b, expires: now.Add(ttl)}
	return tok, nil
}

// Revoke ends every capability of an attempt (settlement, cancellation) and
// reports how many were live. There is no grace: a report not yet admitted when
// the attempt settles is late.
func (g *Grants) Revoke(attemptID string) int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, st := range g.byHash {
		if st.b.AttemptID == attemptID && !st.revoked {
			st.revoked = true
			n++
		}
	}
	return n
}

// Active reports the number of live capabilities (diagnostics and tests).
func (g *Grants) Active() int {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, st := range g.byHash {
		if !st.revoked && st.expires.After(now) {
			n++
		}
	}
	return n
}

// Admit validates one report file written by IPC namespace folder. Unknown,
// revoked, expired and foreign-folder tokens are all CodeStale so a token cannot
// be probed. A live token whose envelope names another run or attempt is a
// spoof (CodeIdentity). On success the result is a provisional record and the
// matching binding; nothing is verified.
func (g *Grants) Admit(folder string, raw []byte) (Provisional, Binding, error) {
	env, err := Parse(raw)
	if err != nil {
		return Provisional{}, Binding{}, err
	}
	now := g.now()
	g.mu.Lock()
	st, ok := g.byHash[sha256.Sum256([]byte(env.RunToken))]
	var b Binding
	live := ok && !st.revoked && st.expires.After(now) && st.b.Folder == folder
	if live {
		b = st.b
	}
	g.mu.Unlock()
	if !live {
		return Provisional{}, Binding{}, reject(CodeStale)
	}
	if env.RunID != b.RunID || env.AttemptID != b.AttemptID {
		return Provisional{}, Binding{}, reject(CodeIdentity)
	}
	if g.validScope != nil && g.validScope(b.Scope) != nil {
		return Provisional{}, Binding{}, reject(CodeScopeChanged)
	}
	p, err := Canonical(env)
	if err != nil {
		return Provisional{}, Binding{}, err
	}
	return p, b, nil
}
