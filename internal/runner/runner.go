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
	d        *Docker
	provider AgentProvider
	paths    Paths
	cfg      SpawnDefaults
	allow    []string // additional env var names beyond provider's RequiredEnvVars
	version  string
	log      *slog.Logger
	store    stateStore // for persisting compact timestamps

	// memory is the long-term memory layer. Optional: if nil, prompts
	// are sent without memory context. Set via SetMemory after
	// construction (same circular-dep pattern as queue).
	memory MemoryCompiler

	// extractFn is the callback for conversation extraction (Phase A).
	extractFn func(ctx context.Context, chatFolder, userMsg, agentReply string)

	// compactFn is the callback for session compaction. Summarizes
	// the conversation and stores it in memory. Set via SetCompactFn.
	compactFn func(ctx context.Context, chatFolder string) error

	// turnCount tracks turns per chat for auto-compact.
	turnCountMu sync.Mutex
	turnCount   map[string]int

	// lastCompact tracks last compact time per chat (for interval/daily triggers).
	// Protected by activityMu (reuses the same lock as activity map).
	lastCompact map[string]time.Time

	// compactAfter triggers auto-compact after N turns. 0 = disabled.
	compactAfter int

	// Per-chat provider overrides (via /provider command).
	providersMu   sync.RWMutex
	chatProviders  map[string]AgentProvider

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
	provider AgentProvider,
	paths Paths,
	defaults SpawnDefaults,
	envAllow []string,
	version string,
	log *slog.Logger,
) (*Runner, error) {
	if d == nil {
		return nil, errors.New("runner: nil Docker")
	}
	if provider == nil {
		return nil, errors.New("runner: nil provider")
	}
	if err := paths.Validate(); err != nil {
		return nil, err
	}
	if defaults.Image == "" {
		defaults.Image = provider.Image()
	}
	if log == nil {
		log = slog.Default()
	}
	return &Runner{
		d:              d,
		provider:       provider,
		paths:          paths,
		cfg:            defaults,
		allow:          envAllow,
		version:        version,
		log:            log,
		containerLocks: make(map[string]*sync.Mutex),
		activity:       make(map[string]time.Time),
		turnCount:      make(map[string]int),
		compactAfter:   30,
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

	// Use per-chat provider for image + env vars.
	prov := r.ProviderForChat(chatFolder)
	envAllow := append(prov.RequiredEnvVars(), r.allow...)

	spawn := SpawnConfig{
		Image:        prov.Image(),
		ChatFolder:   chatFolder,
		Version:      r.version,
		Mounts:       mounts,
		UID:          r.cfg.UID,
		GID:          r.cfg.GID,
		MemoryBytes:  r.cfg.MemoryBytes,
		NanoCPUs:     r.cfg.NanoCPUs,
		PidsLimit:    r.cfg.PidsLimit,
		EnvAllowlist: envAllow,
		HomeDir:      prov.HomeDir(),
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

	// Build memory context for the system prompt via the Phase B
	// Context Compiler (policy-driven selection: pinned → recent
	// decisions → relevant preferences → facts → fallback).
	var opts RunPromptOpts
	if r.memory != nil {
		compiled := r.memory.CompileContext(ctx, chatFolder, prompt)
		if compiled != "" {
			opts.AppendSystemPrompt = compiled
			r.log.Debug("injecting compiled memory context",
				slog.String("chat", chatFolder),
				slog.Int("len", len(compiled)))
		}
	}

	prov := r.ProviderForChat(chatFolder)
	reply, err := r.execWithProvider(ctx, id, prov, prov.RunCmd(prompt, opts.AppendSystemPrompt))
	if err != nil {
		return "", err
	}
	r.touch(chatFolder)

	// Trigger async extraction after successful agent turn.
	if r.memory != nil && r.extractFn != nil && reply != "" {
		go func() {
			bgCtx := context.Background()
			r.extractAndIngest(bgCtx, chatFolder, prompt, reply)
		}()
	}

	// Auto-compact check: if turn count exceeds threshold, compact
	// the session asynchronously. Reset count IMMEDIATELY so
	// concurrent/subsequent turns don't re-trigger.
	turns := r.incrementTurnCount(chatFolder)
	if r.compactAfter > 0 && turns >= r.compactAfter {
		r.resetTurnCount(chatFolder) // reset BEFORE async compact
		r.log.Info("auto-compact triggered",
			slog.String("chat", chatFolder),
			slog.Int("turns", turns))
		go func() {
			bgCtx := context.Background()
			r.CompactSession(bgCtx, chatFolder, tier, allowlist)
		}()
	}

	return reply, nil
}


// RunFresh is like Run but without session continuity. For system
// prompts that should not pollute conversation history.
func (r *Runner) RunFresh(
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
	prov := r.ProviderForChat(chatFolder)
	return r.execWithProvider(ctx, id, prov, prov.RunFreshCmd(prompt))
}

// execWithProvider runs a provider-built command inside a container
// with config restore and error classification.
func (r *Runner) execWithProvider(ctx context.Context, containerID string, prov AgentProvider, cmd []string) (string, error) {
	// Restore config from backup if needed.
	if restoreCmd := prov.RestoreConfigCmd(); restoreCmd != nil {
		r.d.Exec(ctx, containerID, restoreCmd, nil)
	}

	res, err := r.d.Exec(ctx, containerID, cmd, nil)
	if err != nil {
		return "", fmt.Errorf("runner: exec: %w", err)
	}
	if res.ExitCode != 0 {
		stderr := strings.TrimSpace(res.Stderr)
		if prov.IsNotLoggedInError(stderr, res.Stdout) {
			return "", r.classifyAuthError(ctx, containerID)
		}
		return "", fmt.Errorf("runner: agent exited %d (stderr=%q)", res.ExitCode, stderr)
	}
	return strings.TrimSpace(res.Stdout), nil
}

// classifyAuthError distinguishes "never logged in" from "session expired".
func (r *Runner) classifyAuthError(ctx context.Context, containerID string) error {
	status, err := r.checkAuthViaProvider(ctx, containerID)
	if err != nil {
		return ErrNotLoggedIn
	}
	if status.AuthMethod != "" && status.AuthMethod != "none" {
		return ErrSessionExpired
	}
	return ErrNotLoggedIn
}

func (r *Runner) checkAuthViaProvider(ctx context.Context, containerID string) (AuthStatus, error) {
	res, err := r.d.Exec(ctx, containerID, r.provider.AuthStatusCmd(), nil)
	if err != nil {
		return AuthStatus{}, err
	}
	return r.provider.ParseAuthStatus(res.Stdout)
}

// Provider returns the agent provider for a given chat. If a per-chat
// override is set, returns that; otherwise the default provider.
func (r *Runner) Provider() AgentProvider { return r.provider }

// ProviderForChat returns the provider for a specific chat, checking
// per-chat overrides first.
func (r *Runner) ProviderForChat(chatFolder string) AgentProvider {
	r.providersMu.RLock()
	if p, ok := r.chatProviders[chatFolder]; ok {
		r.providersMu.RUnlock()
		return p
	}
	r.providersMu.RUnlock()
	return r.provider
}

// SetChatProvider sets a per-chat provider override and kills the
// existing container so Ensure respawns it with the new provider's image.
func (r *Runner) SetChatProvider(chatFolder string, p AgentProvider) {
	r.providersMu.Lock()
	if r.chatProviders == nil {
		r.chatProviders = make(map[string]AgentProvider)
	}
	r.chatProviders[chatFolder] = p
	r.providersMu.Unlock()

	// Kill the existing container — it has the wrong image.
	name := ContainerName(chatFolder)
	ctx := context.Background()
	if id, _ := r.d.inspectByName(ctx, name); id != "" {
		r.d.Stop(ctx, id, 5*time.Second)
		r.d.Remove(ctx, id)
		r.log.Info("killed container for provider switch",
			slog.String("chat", chatFolder),
			slog.String("new_provider", p.Name()))
	}
}

// ClearChatProvider removes a per-chat override (reverts to default).
func (r *Runner) ClearChatProvider(chatFolder string) {
	r.providersMu.Lock()
	delete(r.chatProviders, chatFolder)
	r.providersMu.Unlock()
}

// CheckAuth probes auth state of the chat's agent container.
func (r *Runner) CheckAuth(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (AuthStatus, error) {
	id, err := r.Ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return AuthStatus{}, err
	}
	return r.checkAuthViaProvider(ctx, id)
}

// stateStore is the interface Runner needs for persisting compact
// timestamps across restarts. Consumer-side interface.
type stateStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
}

// SetStore wires the state store for persisting compact timestamps.
func (r *Runner) SetStore(s stateStore) { r.store = s }

// MemoryCompiler is the interface the Runner needs from the memory
// layer. CompileContext returns the formatted system prompt appendix
// using the Phase B policy-driven selection (kind-based budgets,
// pinned items, recency weighting).
type MemoryCompiler interface {
	CompileContext(ctx context.Context, chatFolder, query string) string
}

// SetMemory wires the memory layer after construction.
func (r *Runner) SetMemory(m MemoryCompiler) { r.memory = m }

// SetExtractFn wires the conversation extraction callback.
func (r *Runner) SetExtractFn(fn func(ctx context.Context, chatFolder, userMsg, agentReply string)) {
	r.extractFn = fn
}

// SetCompactFn wires the session compact callback (summarize → store → clear).
func (r *Runner) SetCompactFn(fn func(ctx context.Context, chatFolder string) error) {
	r.compactFn = fn
}

// SetCompactAfter sets the turn threshold for auto-compact. 0 = disabled.
func (r *Runner) SetCompactAfter(n int) { r.compactAfter = n }

// ClearSession removes Claude Code session files for a chat, forcing
// the next --continue to start a fresh conversation. Memory is NOT
// affected — only the conversation context is cleared.
func (r *Runner) ClearSession(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) error {
	id, err := r.Ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return err
	}
	_, err = r.d.Exec(ctx, id, r.provider.ClearSessionCmd(), nil)
	r.resetTurnCount(chatFolder)
	return err
}

// CompactSession summarizes the current conversation, stores the
// summary in memory, then clears the session. The summary becomes
// the bridge between the old conversation and the new one — the
// Context Compiler injects it via the reserved summary slot.
func (r *Runner) CompactSession(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (string, error) {
	if r.compactFn == nil {
		return "", r.ClearSession(ctx, chatFolder, tier, allowlist)
	}
	if r.getTurnCount(chatFolder) == 0 {
		return "nothing to compact (0 turns)", nil
	}
	if err := r.compactFn(ctx, chatFolder); err != nil {
		r.log.Warn("compact summarize failed, clearing anyway",
			slog.String("chat", chatFolder), slog.Any("err", err))
	}
	if err := r.ClearSession(ctx, chatFolder, tier, allowlist); err != nil {
		return "", err
	}
	return "compacted", nil
}

func (r *Runner) incrementTurnCount(chatFolder string) int {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	r.turnCount[chatFolder]++
	return r.turnCount[chatFolder]
}

func (r *Runner) getTurnCount(chatFolder string) int {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	return r.turnCount[chatFolder]
}

func (r *Runner) resetTurnCount(chatFolder string) {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	r.turnCount[chatFolder] = 0
}

// extractAndIngest is the background callback for Phase A. Runs
// after each successful agent turn — extracts structured facts and
// stores them in memory.
func (r *Runner) extractAndIngest(ctx context.Context, chatFolder, userMsg, agentReply string) {
	if r.extractFn != nil {
		r.extractFn(ctx, chatFolder, userMsg, agentReply)
	}
}

// ChatsDir returns the host-side chats directory path.
func (r *Runner) ChatsDir() string { return r.paths.ChatsDir }

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

