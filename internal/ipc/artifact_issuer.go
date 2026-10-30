package ipc

// Runner-side issuer of per-exec artifact capabilities (CP-003 PR C).
//
// The runner asks for a capability when it starts an interactive agent exec and
// ends it when that exec has fully finished. The issuer decides, never the
// agent: only the owner's private chat qualifies, the destination is the
// conversation scope snapshot taken at that moment, and any missing piece means
// no capability (fail closed). Groups stay closed until CP-020 and CP-019.

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// ScopeSnapshotter is the part of the store the issuer needs.
type ScopeSnapshotter interface {
	SnapshotConversation(ctx context.Context, jid string, thread int, v store.Visibility) (store.ConversationScope, error)
}

// issuerBackstopTTL is the longest an issued capability lives if the runner
// never ends it. The exec end is the real revocation; this only bounds a leak.
const issuerBackstopTTL = MaxGrantTTL - time.Minute

const issuerSnapshotTimeout = 5 * time.Second

// RunIssuer issues capabilities from the same registry the IPC watcher checks.
type RunIssuer struct {
	Caps     *Capabilities
	Scopes   ScopeSnapshotter
	OwnerJID string // "tg:<positive id>": the owner's private chat, the only destination
	ChatsDir string
	Resolve  FolderResolver
	Now      func() time.Time // nil uses time.Now
	Log      *slog.Logger     // nil is silent

	seq atomic.Uint64
}

func noEnd() {}

// ownerPrivateChat reports whether jid is a positive Telegram user id, i.e. a
// private chat. Group and channel ids are negative.
func ownerPrivateChat(jid string) bool {
	id, ok := strings.CutPrefix(jid, "tg:")
	if !ok {
		return false
	}
	n, err := strconv.ParseInt(id, 10, 64)
	return err == nil && n > 0 && strconv.FormatInt(n, 10) == id
}

// IssueArtifact returns a capability for one exec of folder's conversation
// `thread`, and the function that ends it. An empty token means none; end is
// always safe to call.
func (i *RunIssuer) IssueArtifact(folder string, thread int) (string, func()) {
	if i == nil || i.Caps == nil || i.Scopes == nil || i.Resolve == nil || folder == "" || thread < 0 ||
		!ownerPrivateChat(i.OwnerJID) || !filepath.IsAbs(i.ChatsDir) {
		return "", noEnd
	}
	if jid, ok := i.Resolve(folder); !ok || jid != i.OwnerJID {
		return "", noEnd
	}
	ctx, cancel := context.WithTimeout(context.Background(), issuerSnapshotTimeout)
	defer cancel()
	dest, err := i.Scopes.SnapshotConversation(ctx, i.OwnerJID, thread, store.VisibilityConversation)
	if err != nil {
		i.warn("artifact capability not issued: scope snapshot failed", err)
		return "", noEnd
	}
	now := time.Now
	if i.Now != nil {
		now = i.Now
	}
	runRef := fmt.Sprintf("exec-%s-%d", folder, i.seq.Add(1))
	tok, err := i.Caps.Issue(Grant{
		Folder:      folder,
		RunRef:      runRef,
		Actor:       folder,
		Destination: dest,
		Owner:       store.OwnerLegacyAgentIPC,
		OutputRoot:  filepath.Join(i.ChatsDir, folder),
		ExpiresAt:   now().Add(issuerBackstopTTL),
	})
	if err != nil {
		i.warn("artifact capability not issued: grant refused", err)
		return "", noEnd
	}
	var once sync.Once
	return tok, func() { once.Do(func() { i.Caps.EndRun(runRef) }) }
}

func (i *RunIssuer) warn(msg string, err error) {
	if i.Log != nil {
		i.Log.Warn(msg, slog.Any("err", err))
	}
}
