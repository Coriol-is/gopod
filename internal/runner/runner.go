package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/spaceinvaderz/picoclaw/internal/runner/mountsec"
)

// Runner is the high-level facade telegram (and any future control
// plane) talks to when it needs to send a prompt to a chat's agent.
//
// It bundles the Docker client, the per-chat path layout, and the
// fixed spawn parameters (image, uid/gid, resource caps, env
// allowlist) so callers don't have to thread eight things through
// every call site.
//
// One Runner per picoclaw process. Safe for concurrent use across
// chats; per-chat serialization is intentionally NOT here yet — that
// is the job of M3 GroupQueue. Until then a small per-chat sync.Mutex
// inside lastTouchMu serializes EnsureRunning calls for the same chat.
type Runner struct {
	d       *Docker
	paths   Paths
	cfg     SpawnDefaults
	allow   []string // env var names to forward into containers
	version string
	log     *slog.Logger

	// memory is the long-term memory layer. Optional: if nil, prompts
	// are sent without memory context. Set via SetMemory after
	// construction (same circular-dep pattern as queue).
	memory MemorySearcher

	// extractFn is the callback for conversation extraction (Phase A).
	// It calls Memory.ExtractFromTurn + IngestExtracted. Set via
	// SetExtractFn after construction.
	extractFn func(ctx context.Context, chatFolder, userMsg, agentReply string)

	// containerLocksMu serialises EnsureRunning per chat. Without it,
	// two simultaneous messages on the same chat could race two
	// ContainerCreate calls and Docker would reject the second with
	// a name-collision error.
	containerLocksMu sync.Mutex
	containerLocks   map[string]*sync.Mutex

	// activityMu protects the activity map. The map is keyed by chat
	// folder; values are the time of the chat's most recent agent
	// turn. Used by the idle watcher (M6e).
	activityMu sync.Mutex
	activity   map[string]time.Time
}

// SpawnDefaults captures the per-process defaults that go into every
// SpawnConfig the runner constructs. These come from internal/config
// at startup.
type SpawnDefaults struct {
	Image       string
	UID         int
	GID         int
	MemoryBytes int64
	NanoCPUs    int64
	PidsLimit   int64
}

// New constructs a Runner. Pass the Paths struct picoclaw computed at
// config-load time, the SpawnDefaults, the env-var allowlist, and the
// build version (used as the picoclaw.version label on every spawned
// container so CleanupLeftovers can age them out across upgrades).
func New(
	d *Docker,
	paths Paths,
	defaults SpawnDefaults,
	envAllow []string,
	version string,
	log *slog.Logger,
) (*Runner, error) {
	if d == nil {
		return nil, errors.New("runner: nil Docker")
	}
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if defaults.Image == "" {
		return nil, errors.New("runner: empty image")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		d:              d,
		paths:          paths,
		cfg:            defaults,
		allow:          envAllow,
		version:        version,
		log:            log,
		containerLocks: make(map[string]*sync.Mutex),
		activity:       make(map[string]time.Time),
	}, nil
}

// Ensure boots (or re-uses) the container for the given chat folder
// and returns the container id. Idempotent. Per-chat serialised so
// concurrent callers do not race ContainerCreate.
//
// allowlist is the parsed mount allowlist; pass nil if no extras
// are configured (BuildMounts treats nil as an empty allowlist).
func (r *Runner) Ensure(
	ctx context.Context,
	chatFolder string,
	tier Tier,
	allowlist *mountsec.Allowlist,
) (string, error) {
	if chatFolder == "" {
		return "", errors.New("runner: empty chatFolder")
	}
	r.lockChat(chatFolder)
	defer r.unlockChat(chatFolder)

	if err := EnsureChatDirs(r.paths, chatFolder, tier == TierOwner, r.log); err != nil {
		return "", err
	}

	mounts, err := BuildMounts(r.paths, chatFolder, tier, allowlist)
	if err != nil {
		return "", err
	}

	spawn := SpawnConfig{
		Image:        r.cfg.Image,
		ChatFolder:   chatFolder,
		Version:      r.version,
		Mounts:       mounts,
		UID:          r.cfg.UID,
		GID:          r.cfg.GID,
		MemoryBytes:  r.cfg.MemoryBytes,
		NanoCPUs:     r.cfg.NanoCPUs,
		PidsLimit:    r.cfg.PidsLimit,
		EnvAllowlist: r.allow,
	}
	conf, host, name, err := BuildContainerArgs(spawn)
	if err != nil {
		return "", err
	}

	id, err := r.d.EnsureRunning(ctx, name, conf, host)
	if err != nil {
		return "", err
	}
	return id, nil
}

// Run is the high-level "telegram message → claude reply" entry. It
// ensures the chat's container is running, runs the prompt via
// claude -p, and returns the reply.
//
// Maps ErrNotLoggedIn through unchanged so the telegram handler can
// distinguish "needs /login" from real failures.
func (r *Runner) Run(
	ctx context.Context,
	chatFolder string,
	tier Tier,
	allowlist *mountsec.Allowlist,
	prompt string,
) (string, error) {
	id, err := r.Ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return "", fmt.Errorf("runner: ensure %q: %w", chatFolder, err)
	}
	r.touch(chatFolder)

	// Build memory context for the system prompt if memory is wired.
	var opts RunPromptOpts
	if r.memory != nil {
		memories, err := r.memory.Search(ctx, chatFolder, prompt, 5)
		if err != nil {
			r.log.Warn("memory search failed, proceeding without context",
				slog.String("chat", chatFolder),
				slog.Any("err", err))
		} else if len(memories) > 0 {
			opts.AppendSystemPrompt = formatMemoryContext(memories)
			r.log.Debug("injecting memory context",
				slog.String("chat", chatFolder),
				slog.Int("memories", len(memories)))
		}
	}

	reply, err := r.d.RunPrompt(ctx, id, prompt, opts)
	if err != nil {
		return "", err
	}
	r.touch(chatFolder)

	// Trigger async extraction after successful agent turn.
	// Fire-and-forget: extraction failures must not affect the reply.
	if r.memory != nil && r.extractFn != nil && reply != "" {
		go func() {
			bgCtx := context.Background()
			r.extractAndIngest(bgCtx, chatFolder, prompt, reply)
		}()
	}

	return reply, nil
}

// formatMemoryContext builds the --append-system-prompt text from
// retrieved memories.
func formatMemoryContext(memories []MemoryItem) string {
	var sb strings.Builder
	sb.WriteString("Here are relevant facts from previous conversations with this user. Use them to personalize your response when relevant, but do not mention that you are reading from a memory system unless asked:\n\n")
	for i, m := range memories {
		if m.Title != "" {
			fmt.Fprintf(&sb, "%d. [%s] %s: %s\n", i+1, m.Kind, m.Title, m.Content)
		} else {
			fmt.Fprintf(&sb, "%d. [%s] %s\n", i+1, m.Kind, m.Content)
		}
	}
	return sb.String()
}

// CheckAuth proxies through to docker.CheckAuth so the telegram
// handler can probe auth state without holding a Docker reference.
func (r *Runner) CheckAuth(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (AuthStatus, error) {
	id, err := r.Ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return AuthStatus{}, err
	}
	return r.d.CheckAuth(ctx, id)
}

// MemorySearcher is the interface the Runner needs from the memory
// layer. Defined here (consumer-side) to avoid importing
// internal/memory into internal/runner.
type MemorySearcher interface {
	Search(ctx context.Context, chatFolder, query string, k int) ([]MemoryItem, error)
}

// MemoryItem is the minimal view of a memory record the Runner needs
// to format the system prompt appendix. Mirrors memory.Item.
type MemoryItem struct {
	Kind    string
	Title   string
	Content string
}

// SetMemory wires the memory layer after construction.
func (r *Runner) SetMemory(m MemorySearcher) { r.memory = m }

// SetExtractFn wires the conversation extraction callback.
func (r *Runner) SetExtractFn(fn func(ctx context.Context, chatFolder, userMsg, agentReply string)) {
	r.extractFn = fn
}

// extractAndIngest is the background callback for Phase A. Runs
// after each successful agent turn — extracts structured facts and
// stores them in memory.
func (r *Runner) extractAndIngest(ctx context.Context, chatFolder, userMsg, agentReply string) {
	if r.extractFn != nil {
		r.extractFn(ctx, chatFolder, userMsg, agentReply)
	}
}

// Docker returns the underlying Docker client handle. Used by
// login.go to call ExecInteractive directly (the login flow needs
// the raw interactive exec primitive, not the high-level Run facade).
func (r *Runner) Docker() *Docker { return r.d }

// LastActivity returns the timestamp of the chat's most recent agent
// turn, or the zero time if the chat has never been touched. Used by
// the idle watcher.
func (r *Runner) LastActivity(chatFolder string) time.Time {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	return r.activity[chatFolder]
}

// ActiveChats returns the set of chat folders that have been touched
// at least once in this picoclaw process. Used by the idle watcher
// at each tick.
func (r *Runner) ActiveChats() []string {
	r.activityMu.Lock()
	defer r.activityMu.Unlock()
	out := make([]string, 0, len(r.activity))
	for k := range r.activity {
		out = append(out, k)
	}
	return out
}

// touch records that the chat just had agent activity. Called from
// Run() before and after the prompt so a long-running prompt cannot
// be killed by the idle watcher mid-flight.
func (r *Runner) touch(chatFolder string) {
	r.activityMu.Lock()
	r.activity[chatFolder] = time.Now()
	r.activityMu.Unlock()
}

// lockChat / unlockChat manage the per-chat sync.Mutex map. The
// indirection lets us serialise Ensure calls for the same chat
// without holding a global lock that would serialise across chats.
func (r *Runner) lockChat(chatFolder string) {
	r.containerLocksMu.Lock()
	mu, ok := r.containerLocks[chatFolder]
	if !ok {
		mu = &sync.Mutex{}
		r.containerLocks[chatFolder] = mu
	}
	r.containerLocksMu.Unlock()
	mu.Lock()
}

func (r *Runner) unlockChat(chatFolder string) {
	r.containerLocksMu.Lock()
	mu := r.containerLocks[chatFolder]
	r.containerLocksMu.Unlock()
	if mu != nil {
		mu.Unlock()
	}
}

