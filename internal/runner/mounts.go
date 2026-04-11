// Package runner is picoclaw's container lifecycle and agent runtime.
//
// The package is split along security-critical boundaries:
//
//   - runner/mountsec — allowlist schema, blocked patterns, symlink and
//     traversal validation. Pure code, no Docker SDK. Every rule here
//     is motivated by a threat in docs/ISOLATION.md §1.
//   - runner/mounts.go — per-chat mount construction from the three
//     trust tiers in docs/ISOLATION.md §3, consuming a
//     mountsec.Allowlist for operator-added extras. Returns picoclaw's
//     local Mount spec; the Docker SDK layer converts at spawn time.
//   - runner/docker_args.go — ContainerCreate argument assembly from
//     docs/ISOLATION.md §6 (RO root, dropped caps, non-root uid, pids
//     limit, mem/cpu caps, tmpfs trio).
//   - runner/docker.go — Docker SDK client wiring, exec attach, demux.
//   - runner/lifecycle.go — idle watcher, leftover cleanup.
//
// This file covers mount construction only.
package runner

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"github.com/spaceinvaderz/picoclaw/internal/runner/chattmpl"
	"github.com/spaceinvaderz/picoclaw/internal/runner/mountsec"
)

// Tier is the trust tier of a chat per docs/ISOLATION.md §2.
//
// There is no TierUnregistered here: unregistered chats never get a
// container at all, so they never reach BuildMounts.
type Tier int

const (
	// TierOwner is the PICOCLAW_OWNER_CHAT_ID chat. Gets project root
	// (RO), store.sqlite (RW), and the full extras allowlist at the
	// operator's declared mode.
	TierOwner Tier = iota

	// TierRegistered is any other registered chat. Gets its own workspace,
	// memory, ipc, and session directories only. Extras land as RO unless
	// the allowlist entry explicitly opts in to RW via NonOwnerReadOnly=false.
	TierRegistered
)

// Mount is picoclaw's internal bind-mount spec. It describes one
// host-to-container path mapping. The Docker SDK layer in docker_args.go
// converts this slice to []*mount.Mount at spawn time so that
// internal/runner/mountsec and this file can stay Docker-SDK-free for
// unit testing.
type Mount struct {
	// Source is an absolute host path. Must exist by spawn time.
	Source string
	// Target is an absolute container path.
	Target string
	// ReadOnly controls the bind-mount ro flag.
	ReadOnly bool
}

// Paths groups the host-side path roots picoclaw uses when constructing
// mounts. All fields must be absolute.
//
// Why these five and no more: every per-chat target (chat workspace,
// memory, ipc, session) derives from ChatsDir + chatFolder or
// DataDir + chatFolder. Every cross-chat target (project root,
// store.sqlite, container skills) derives from RepoRoot, DataDir, or
// ContainerSkillsDir. Five roots are sufficient.
type Paths struct {
	RepoRoot           string // picoclaw checkout root
	DataDir            string // ${PICOCLAW_DATA_DIR}
	ChatsDir           string // typically ${RepoRoot}/chats
	ContainerSkillsDir string // typically ${RepoRoot}/container/skills
	// EmptyFile is an absolute path to an empty, operator-writable file
	// picoclaw uses to mask ${REPO_ROOT}/.env inside the owner's project
	// mount. EnsureChatDirs creates it at bootstrap.
	EmptyFile string
}

// Validate checks that every field of Paths is absolute. It does NOT
// stat the paths — that is EnsureChatDirs's job.
func (p Paths) Validate() error {
	for _, pair := range []struct {
		name, val string
	}{
		{"RepoRoot", p.RepoRoot},
		{"DataDir", p.DataDir},
		{"ChatsDir", p.ChatsDir},
		{"ContainerSkillsDir", p.ContainerSkillsDir},
		{"EmptyFile", p.EmptyFile},
	} {
		if pair.val == "" {
			return fmt.Errorf("mounts: Paths.%s is empty", pair.name)
		}
		if !filepath.IsAbs(pair.val) {
			return fmt.Errorf("mounts: Paths.%s = %q must be absolute", pair.name, pair.val)
		}
	}
	return nil
}

// BuildMounts constructs the per-chat mount list for the given trust
// tier and chat folder. The returned slice is ordered: standard mounts
// first (in docs/ISOLATION.md §3 order), then allowlist extras in the
// order they appear in the allowlist file.
//
// BuildMounts is pure: it does not touch the filesystem. Callers are
// expected to have run EnsureChatDirs first so that every Source path
// exists by the time Docker tries to bind-mount it.
//
// Passing a nil allowlist is equivalent to an empty allowlist.
func BuildMounts(p Paths, chatFolder string, tier Tier, al *mountsec.Allowlist) ([]Mount, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	if chatFolder == "" {
		return nil, errors.New("mounts: empty chatFolder")
	}

	out := standardMounts(p, chatFolder, tier)

	if al != nil {
		out = append(out, extraMountsForChat(chatFolder, tier, al)...)
	}

	return out, nil
}

// standardMounts returns the ISOLATION.md §3.1 / §3.2 standard mount set
// for the tier.
//
// Owner adds three extras on top of the non-owner baseline:
//  1. RepoRoot → /workspace/project (RO)
//  2. (optional) EmptyFile → /workspace/project/.env (RO) — masks .env
//     if and only if RepoRoot/.env exists on the host. Docker cannot
//     create a mountpoint inside a RO bind mount, so trying to nest
//     the mask mount when there is no real .env to overlay produces
//     `make mountpoint .../workspace/project/.env: read-only file
//     system` and the spawn fails. If there is no .env to begin with
//     there is also nothing to mask, so skipping is safe.
//  3. store.sqlite → /workspace/store/store.sqlite (RW) — direct SQL
//     access for the owner.
func standardMounts(p Paths, chatFolder string, tier Tier) []Mount {
	chatDir := filepath.Join(p.ChatsDir, chatFolder)
	memoryDir := filepath.Join(chatDir, "memory")
	ipcDir := filepath.Join(p.DataDir, "ipc", chatFolder)
	sessionDir := filepath.Join(p.DataDir, "sessions", chatFolder, ".claude")

	var out []Mount

	if tier == TierOwner {
		// Project root for self-modification (/release, debugging). RO
		// so the agent cannot corrupt the picoclaw checkout directly —
		// writes go through IPC.
		out = append(out, Mount{
			Source:   p.RepoRoot,
			Target:   "/workspace/project",
			ReadOnly: true,
		})
		// Mask the .env inside the project mount IF it exists. The
		// stat call is the one filesystem hit BuildMounts performs;
		// it stays "pure-ish" (deterministic given the host fs state)
		// and the alternative is a hard spawn failure for any
		// operator who runs picoclaw out of a checkout without .env.
		if envExists(filepath.Join(p.RepoRoot, ".env")) {
			out = append(out, Mount{
				Source:   p.EmptyFile,
				Target:   "/workspace/project/.env",
				ReadOnly: true,
			})
		}
		// Direct SQL access for advanced debugging. Acknowledged risk:
		// agent in owner chat can corrupt the store. See D006 / ISOLATION.md §3.1.
		out = append(out, Mount{
			Source:   filepath.Join(p.DataDir, "store.sqlite"),
			Target:   "/workspace/store/store.sqlite",
			ReadOnly: false,
		})
	}

	// Baseline mounts — every registered chat, owner or not.
	out = append(out,
		Mount{Source: chatDir, Target: "/workspace/chat", ReadOnly: false},
		Mount{Source: memoryDir, Target: "/workspace/memory", ReadOnly: false},
		Mount{Source: ipcDir, Target: "/workspace/ipc", ReadOnly: false},
		Mount{Source: sessionDir, Target: "/home/node/.claude", ReadOnly: false},
		Mount{Source: p.ContainerSkillsDir, Target: "/home/node/.claude/skills", ReadOnly: true},
	)

	return out
}

// extraMountsForChat returns the allowlist extras the given chat is
// permitted to mount, with the per-tier read-only filter applied.
func extraMountsForChat(chatFolder string, tier Tier, al *mountsec.Allowlist) []Mount {
	out := make([]Mount, 0, len(al.ExtraMounts))
	isOwner := tier == TierOwner

	for _, m := range al.ExtraMounts {
		if !chatMatches(chatFolder, m.AllowedChats) {
			continue
		}

		readOnly := m.Mode == "ro"
		// Non-owner chats get the read-only override unless the entry
		// explicitly opts out with non_owner_read_only: false.
		if !isOwner && m.EffectiveNonOwnerReadOnly() {
			readOnly = true
		}

		out = append(out, Mount{
			Source:   m.HostPath,
			Target:   m.ContainerPath,
			ReadOnly: readOnly,
		})
	}
	return out
}

// chatMatches reports whether a chatFolder is allowed by an AllowedChats
// list. The wildcard "*" matches any non-empty chat folder. An empty
// list (AllowedChats: []) means owner-only, so it matches nothing at
// this layer — the owner gets the extra via tier-default logic upstream
// only if the list contains the owner's folder or "*". Validation
// ensures AllowedChats is non-nil.
func chatMatches(chatFolder string, allowed []string) bool {
	for _, a := range allowed {
		if a == "*" || a == chatFolder {
			return true
		}
	}
	return false
}

// EnsureChatDirs creates the host-side directories a chat's mounts will
// point at, plus the shared EmptyFile if it doesn't already exist, plus
// the seeded CLAUDE.md / memory/MEMORY.md template files (only on first
// creation — never overwritten). This is the one side-effecting
// function in mounts.go; callers run it once before BuildMounts when
// onboarding a chat or before spawning a container for the first time
// after picoclaw restart.
//
// isOwner toggles the owner-specific blurb in the seeded CLAUDE.md.
//
// log is used to surface "seeded N file(s)" once per first-creation;
// pass nil to fall back to slog.Default.
//
// Idempotent: safe to call repeatedly on the same chatFolder. Files
// created on prior runs are left alone.
func EnsureChatDirs(p Paths, chatFolder string, isOwner bool, log *slog.Logger) error {
	if err := p.Validate(); err != nil {
		return err
	}
	if chatFolder == "" {
		return errors.New("mounts: empty chatFolder")
	}
	if log == nil {
		log = slog.Default()
	}

	chatDir := filepath.Join(p.ChatsDir, chatFolder)
	memoryDir := filepath.Join(chatDir, "memory")

	dirs := []string{
		chatDir,
		memoryDir,
		filepath.Join(p.DataDir, "ipc", chatFolder),
		filepath.Join(p.DataDir, "sessions", chatFolder, ".claude"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("mounts: mkdir %q: %w", d, err)
		}
	}

	// Ensure the mask file exists (owner .env override). Zero-byte,
	// mode 0644 — Docker will bind-mount it over /workspace/project/.env
	// inside the container as a RO file.
	if err := ensureEmptyFile(p.EmptyFile); err != nil {
		return err
	}

	// Seed CLAUDE.md and memory/MEMORY.md from embedded templates
	// the very first time this chat folder is created. Never
	// overwrites existing files — operator and agent edits win.
	created, err := chattmpl.Seed(chatDir, memoryDir, chattmpl.Vars{
		ChatFolder: chatFolder,
		IsOwner:    isOwner,
	})
	if err != nil {
		return fmt.Errorf("mounts: seed templates: %w", err)
	}
	if len(created) > 0 {
		log.Info("seeded chat workspace templates",
			slog.String("chat", chatFolder),
			slog.Int("files", len(created)))
	}
	return nil
}

// envExists reports whether path is a regular file (not a dir, not
// missing). Used by standardMounts to decide whether to emit the
// .env mask mount for the owner tier.
func envExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode().IsRegular()
}

// ensureEmptyFile makes sure path is a regular empty file. If path
// exists and is non-empty, it is NOT truncated — the function errors
// out so an operator-edited mask file is never silently clobbered.
func ensureEmptyFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mounts: mkdir parent of %q: %w", path, err)
	}
	info, err := os.Stat(path)
	if err == nil {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("mounts: %q exists but is not a regular file", path)
		}
		if info.Size() != 0 {
			return fmt.Errorf("mounts: %q exists but is not empty (size %d)", path, info.Size())
		}
		return nil
	}
	if !os.IsNotExist(err) {
		return fmt.Errorf("mounts: stat %q: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("mounts: create %q: %w", path, err)
	}
	return f.Close()
}
