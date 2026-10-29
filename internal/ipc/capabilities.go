package ipc

// Per-run artifact capabilities (CP-003 PR 4).
//
// A capability is an opaque host-issued token bound to one run: the IPC folder
// (actor), the destination ConversationScope (and so its ScopeRef), the delivery
// owner, the run's ordinary output root and an expiry. No JSON field in a
// request can widen any of those. Capabilities live in memory only: a restart
// or the end of the run revokes them, and a request that arrives afterwards
// fails closed. Accepted work is not revoked: once a request is committed to
// the outbox it is governed by dispatch-time scope validation instead.

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

// ContainerOutputRoot is where the run sees its ordinary output root.
const ContainerOutputRoot = "/workspace/chat"

var (
	// ErrStaleCapability covers unknown, revoked, expired and wrong-folder
	// tokens alike, so a token cannot be probed for which of those it is.
	ErrStaleCapability = errors.New("ipc: capability is not active")
	// ErrOutputRootDenied is a host path that is not an ordinary output root.
	ErrOutputRootDenied = errors.New("ipc: not an ordinary output root")
	// ErrPathDenied is an agent path outside the run's ordinary output root or
	// naming protected material.
	ErrPathDenied = errors.New("ipc: path denied")
)

// Grant is what the host decided for one run.
type Grant struct {
	Folder        string // IPC namespace folder of the run (the actor)
	RunRef        string // opaque run identity used to revoke at run end
	Actor         string
	Destination   store.ConversationScope
	Owner         store.DeliveryOwner
	OutputRoot    string // host directory behind ContainerOutputRoot
	ContainerRoot string // defaults to ContainerOutputRoot
	ExpiresAt     time.Time
}

// OutputRootPolicy decides which host directories may be an output root. Only
// the ordinary per-chat workspace directly below ChatsDir qualifies. Protected
// roots (CP-020 shared namespaces with their memory/ipc/native/policy siblings,
// IPC, sessions, snapshots, artifact blobs, backups and the database directory)
// and anything the mount deny list blocks are refused, and group workspaces are
// not reachable through this policy at all: groups stay closed until CP-020
// isolation and CP-019 visibility acceptance.
type OutputRootPolicy struct {
	DataDir  string
	ChatsDir string
}

var protectedDataNames = []string{"shared-namespaces", "ipc", "sessions", "shared-tool-snapshots", "artifacts", "backups", "memory"}

var folderNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`)

// Validate checks a host path against the policy.
func (p OutputRootPolicy) Validate(root string) error {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !filepath.IsAbs(p.ChatsDir) || filepath.Clean(p.ChatsDir) != p.ChatsDir ||
		!filepath.IsAbs(p.DataDir) || filepath.Clean(p.DataDir) != p.DataDir {
		return ErrOutputRootDenied
	}
	if filepath.Dir(root) != p.ChatsDir || !folderNamePattern.MatchString(filepath.Base(root)) {
		return ErrOutputRootDenied
	}
	if mountsec.IsBlocked(root) {
		return ErrOutputRootDenied
	}
	// The root must not be, contain, or sit inside any protected host tree.
	if within(root, p.DataDir) {
		return ErrOutputRootDenied
	}
	for _, n := range protectedDataNames {
		prot := filepath.Join(p.DataDir, n)
		if within(prot, root) || within(root, prot) {
			return ErrOutputRootDenied
		}
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrOutputRootDenied
	}
	return nil
}

// within reports whether child equals parent or lies below it.
func within(parent, child string) bool {
	rel, err := filepath.Rel(parent, child)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

var deniedSuffixes = []string{".sqlite", ".sqlite3", ".db", ".sqlite-wal", ".sqlite-shm", ".db-wal", ".db-shm", "-wal", "-shm", ".wal"}

// MapPath translates an agent path under the container output root into a
// root-relative path. It never accepts a host path, a path outside the root,
// hidden trees, the memory tree, database/WAL files or anything the mount deny
// list blocks. This is path policy, not content scanning: a secret copied into
// an innocent file is not detected.
func (g Grant) MapPath(agentPath string) (string, error) {
	croot := g.ContainerRoot
	if croot == "" {
		croot = ContainerOutputRoot
	}
	if path.Clean(agentPath) != agentPath || !strings.HasPrefix(agentPath, croot+"/") || strings.ContainsRune(agentPath, 0) {
		return "", ErrPathDenied
	}
	rel := strings.TrimPrefix(agentPath, croot+"/")
	parts := strings.Split(rel, "/")
	for i, c := range parts {
		lc := strings.ToLower(c)
		if c == "" || strings.HasPrefix(c, ".") || (i == 0 && lc == "memory") {
			return "", ErrPathDenied
		}
		for _, suf := range deniedSuffixes {
			if strings.HasSuffix(lc, suf) {
				return "", ErrPathDenied
			}
		}
	}
	if mountsec.IsBlocked(filepath.Join(g.OutputRoot, filepath.FromSlash(rel))) {
		return "", ErrPathDenied
	}
	return rel, nil
}

// Defaults for the registry.
const (
	// MaxGrantTTL caps how long one grant may live.
	MaxGrantTTL = 6 * time.Hour
	// DefaultRevokeGrace is how long after EndRun a request that was written
	// while the run was still active is still admitted.
	DefaultRevokeGrace = 2 * time.Minute
)

// Capabilities is the in-memory registry of active run capabilities.
type Capabilities struct {
	// Grace is the post-EndRun window for requests written before the run ended.
	Grace time.Duration

	policy  OutputRootPolicy
	now     func() time.Time
	resolve FolderResolver
	mu      sync.Mutex
	byHash  map[[32]byte]*capState
}

type capState struct {
	grant     Grant
	revoked   bool
	revokedAt time.Time // zero for an immediate (policy) revocation: no grace
}

// NewCapabilities returns an empty registry. A nil now uses time.Now. resolve
// maps an IPC folder to its registered chat JID; Issue refuses a grant whose
// output root or destination is not the actor's own chat.
func NewCapabilities(policy OutputRootPolicy, now func() time.Time, resolve FolderResolver) *Capabilities {
	if now == nil {
		now = time.Now
	}
	return &Capabilities{Grace: DefaultRevokeGrace, policy: policy, now: now, resolve: resolve, byHash: make(map[[32]byte]*capState)}
}

// Issue validates a grant and returns its opaque token. The token carries no
// information; everything it stands for is held here. The output root must be
// the actor's own chat directory and the destination its own registered chat.
func (c *Capabilities) Issue(g Grant) (string, error) {
	now := c.now()
	if g.Folder == "" || g.RunRef == "" || g.Actor == "" || !g.ExpiresAt.After(now) || g.ExpiresAt.After(now.Add(MaxGrantTTL)) {
		return "", fmt.Errorf("ipc: invalid capability grant")
	}
	if g.Owner != store.OwnerLegacyAgentIPC && g.Owner != store.OwnerControllerResult {
		return "", fmt.Errorf("ipc: invalid capability delivery owner")
	}
	if !store.ValidScopeIdentity(g.Destination.Scope) || g.Destination.ThreadID < 0 ||
		(g.Destination.Visibility != store.VisibilityConversation && g.Destination.Visibility != store.VisibilityPrivate) {
		return "", fmt.Errorf("ipc: invalid capability destination")
	}
	if err := c.policy.Validate(g.OutputRoot); err != nil {
		return "", err
	}
	if filepath.Base(g.OutputRoot) != g.Folder {
		return "", fmt.Errorf("%w: output root is not the actor's chat directory", ErrOutputRootDenied)
	}
	if c.resolve == nil {
		return "", fmt.Errorf("ipc: capability registry has no folder resolver")
	}
	if jid, ok := c.resolve(g.Folder); !ok || jid != g.Destination.Scope.JID {
		return "", fmt.Errorf("ipc: capability destination is not the actor's registered chat")
	}
	var raw [24]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("ipc: capability token: %w", err)
	}
	token := hex.EncodeToString(raw[:])
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pruneLocked(now)
	c.byHash[sha256.Sum256([]byte(token))] = &capState{grant: g}
	return token, nil
}

// pruneLocked drops expired grants and revoked grants past their grace window.
func (c *Capabilities) pruneLocked(now time.Time) {
	for k, st := range c.byHash {
		if !st.grant.ExpiresAt.After(now) || (st.revoked && !now.Before(st.revokedAt.Add(c.Grace))) {
			delete(c.byHash, k)
		}
	}
}

// Resolve returns the grant for a token presented by the given IPC folder. Any
// failure is ErrStaleCapability.
func (c *Capabilities) Resolve(token, folder string) (Grant, error) {
	return c.ResolveAt(token, folder, time.Time{})
}

// ResolveAt is Resolve for a request file last written at `written`. A grant
// that EndRun revoked is still honoured, for Grace, for a file written before
// the revocation: an agent whose last action was to write the request and
// exit must not lose it to the poll interval. A file written after the run
// ended, an unknown write time and a policy revocation (RevokeFolder) fail.
func (c *Capabilities) ResolveAt(token, folder string, written time.Time) (Grant, error) {
	if token == "" || len(token) > 128 {
		return Grant{}, ErrStaleCapability
	}
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	st, ok := c.byHash[sha256.Sum256([]byte(token))]
	if !ok || st.grant.Folder != folder || !st.grant.ExpiresAt.After(now) {
		return Grant{}, ErrStaleCapability
	}
	if st.revoked {
		if st.revokedAt.IsZero() || written.IsZero() || written.After(st.revokedAt) || !now.Before(st.revokedAt.Add(c.Grace)) {
			return Grant{}, ErrStaleCapability
		}
	}
	return st.grant, nil
}

// EndRun revokes every capability of a run (completion, cancellation) and
// reports how many were active. Requests already committed to the outbox are
// unaffected; a request written before this call is still admitted for Grace;
// anything written later fails closed with a stale capability.
func (c *Capabilities) EndRun(runRef string) int {
	now := c.now()
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, st := range c.byHash {
		if st.grant.RunRef == runRef && !st.revoked {
			st.revoked, st.revokedAt = true, now
			n++
		}
	}
	c.pruneLocked(now)
	return n
}

// RevokeFolder revokes every capability of an IPC folder at once, with no
// grace (policy change).
func (c *Capabilities) RevokeFolder(folder string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, st := range c.byHash {
		if st.grant.Folder == folder && !st.revoked {
			st.revoked, st.revokedAt = true, time.Time{}
			n++
		}
	}
	return n
}
