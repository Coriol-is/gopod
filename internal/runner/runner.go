package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
)

// Runner is the high-level facade telegram (and any future control
// plane) talks to when it needs to send a prompt to a chat's agent.
//
// It bundles the Docker client, the per-chat path layout, and the
// fixed spawn parameters (image, uid/gid, resource caps, env
// allowlist) so callers don't have to thread eight things through
// every call site.
//
// One Runner per gopod process. Safe for concurrent use across
// chats; per-chat serialization is intentionally NOT here yet — that
// is the job of M3 GroupQueue. Until then a small per-chat sync.Mutex
// inside lastTouchMu serializes EnsureRunning calls for the same chat.
type Runner struct {
	profilePolicy *admissionPolicy
	groups        GroupRuntime
	d             *Docker
	ex            agentExecer // r.d in production; the agent exec seam for tests
	provider      AgentProvider
	paths         Paths
	cfg           SpawnDefaults
	allow         []string // additional env var names beyond provider's RequiredEnvVars
	version       string
	log           *slog.Logger
	store         stateStore // compact timestamps and conversation sessions

	// ensureFn replaces Ensure in tests (nil = r.Ensure).
	ensureFn func(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (string, error)

	// memory is the long-term memory layer. Optional: if nil, prompts
	// are sent without memory context. Set via SetMemory after
	// construction (same circular-dep pattern as queue).
	memory MemoryCompiler

	// extractFn is the callback for conversation extraction (Phase A).
	// thread is the turn's conversation; its facts are stored there (D023).
	extractFn func(ctx context.Context, chatFolder string, thread int, userMsg, agentReply string)

	// compactFn is the callback for session compaction. Summarizes
	// one conversation and stores it in memory. Set via SetCompactFn.
	compactFn func(ctx context.Context, chatFolder string, thread int) error

	// turnCount tracks turns per conversation for auto-compact.
	turnCountMu sync.Mutex
	turnCount   map[convKey]int

	// lastCompact tracks last compact time per conversation (for
	// interval/daily triggers). Protected by activityMu (reuses the
	// same lock as activity map).
	lastCompact map[convKey]time.Time

	// compactAfter triggers auto-compact after N turns. 0 = disabled.
	compactAfter int

	// progress runs streamed turns in structured mode when the
	// provider supports it (StructuredStreamer), so tool calls reach
	// AgentStream.Progress. Off: plain text, as before (GOPOD_PROGRESS=0).
	progress bool

	// managed is the CP-004 managed task runtime; nil keeps every legacy path.
	managed *managedState

	// Per-chat provider and model overrides (/provider, /model).
	providersMu          sync.RWMutex
	chatProviders        map[string]AgentProvider
	chatModels           map[string]string
	selectionBlocks      map[string]bool
	startupObservations  map[string]StartupObservation
	selectionGenerations map[string]uint64

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

	// caps mints the per-exec memory capability (CP-001). Nil = none.
	caps CapabilityMinter

	// artifacts issues the per-exec artifact delivery capability (CP-003). Nil = none.
	artifacts atomic.Pointer[artifactIssuerBox]

	// versionLogged records container ids whose agent CLI version has
	// been logged, so Ensure logs it once per spawn, not once per turn.
	versionLoggedMu sync.Mutex
	versionLogged   map[string]bool

	// agentSlots marks chats with a streamed agent process alive (see
	// acquireAgentSlot). The channel closes when the process is gone.
	agentSlotsMu       sync.Mutex
	agentSlots         map[string]chan struct{}
	backgroundCancels  map[string]context.CancelFunc
	interactiveWaiters map[string]int

	// gate is the global agent admission gate (gate.go); nodeHeapMB
	// caps Claude agents' V8 heap (0 = off). Set by SetAgentLimits.
	gate       *agentGate
	nodeHeapMB int
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

// New constructs a Runner. Pass the Paths struct gopod computed at
// config-load time, the SpawnDefaults, the env-var allowlist, and the
// build version (used as the gopod.version label on every spawned
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
		ex:             d,
		provider:       provider,
		paths:          paths,
		cfg:            defaults,
		allow:          envAllow,
		version:        version,
		log:            log,
		containerLocks: make(map[string]*sync.Mutex),
		activity:       make(map[string]time.Time),
		turnCount:      make(map[convKey]int),
		compactAfter:   30,
		gate:           newGate(DefaultMaxAgents, DefaultMinAvailableMB, readMemAvailableMB, log),
	}, nil
}

// ensure is Ensure, or the test seam ensureFn.
func (r *Runner) ensure(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (string, error) {
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return "", err
	}
	tier = TierOwner
	if r.selectionBlocked(chatFolder) {
		return "", errors.New("runner: provider selection unavailable")
	}
	if r.ensureFn != nil {
		return r.ensureFn(ctx, chatFolder, tier, allowlist)
	}
	// Internal callers (turns, auth checks, clears) hold the folder's agent slot.
	return r.ensureContainer(ctx, chatFolder, tier, allowlist, false, true)
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
	// A public caller holds no slot, so the managed guard will not recycle a
	// running container for it; with the runtime off nothing differs.
	return r.ensureContainer(ctx, chatFolder, tier, allowlist, false, false)
}

func (r *Runner) ensureContainer(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist, reconcile, slotHeld bool) (string, error) {
	if _, group := r.groupRegistration(ctx, chatFolder); group {
		return "", ErrProfileUnavailable
	}
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return "", err
	}
	tier = TierOwner
	if r.selectionBlocked(chatFolder) {
		return "", errors.New("runner: provider selection unavailable")
	}
	if chatFolder == "" {
		return "", errors.New("runner: empty chatFolder")
	}
	if r.managed != nil {
		// Managed runtime: the foreground container goes through the same policy
		// guard as a task, so roles cannot leak into each other.
		prov := r.ProviderForChat(chatFolder)
		id, err := r.ensureRole(ctx, chatFolder, RoleForegroundPersonal, prov, allowlist, reconcile, slotHeld, "")
		if err != nil {
			return "", err
		}
		r.logAgentVersion(ctx, id, chatFolder, prov)
		return id, nil
	}
	r.lockChat(chatFolder)
	defer r.unlockChat(chatFolder)
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return "", err
	}

	if err := EnsureChatDirs(r.paths, chatFolder, tier == TierOwner, r.log); err != nil {
		return "", err
	}

	mounts, err := BuildMounts(r.paths, chatFolder, tier, allowlist)
	if err != nil {
		return "", err
	}

	// Use per-chat provider for image + env vars.
	prov := r.ProviderForChat(chatFolder)
	envAllow := containerEnvAllow(tier, prov, r.allow)

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

	id, err := r.d.ensureRunning(ctx, name, conf, host, reconcile)
	if err != nil {
		return "", err
	}
	r.logAgentVersion(ctx, id, chatFolder, prov)
	return id, nil
}

// Run is the high-level "telegram message → claude reply" entry. It
// ensures the chat's container is running, runs the prompt in the
// conversation (chatFolder, thread) — thread 0 is the main chat — and
// returns the reply.
//
// Maps ErrNotLoggedIn through unchanged so the telegram handler can
// distinguish "needs /login" from real failures.
func (r *Runner) Run(
	ctx context.Context,
	chatFolder string,
	thread int,
	tier Tier,
	allowlist *mountsec.Allowlist,
	prompt string,
) (reply string, retErr error) {
	_, group := r.groupRegistration(ctx, chatFolder)
	defer func() {
		if group {
			reply = redactGroupCapabilities(reply)
			retErr = groupPublicError(retErr)
		}
	}()
	if err := r.authorizeTurn(ctx, chatFolder); err != nil {
		return "", err
	}
	if err := r.checkGroupInput(ctx, chatFolder, prompt); err != nil {
		return "", err
	}
	releaseSlot, err := r.acquireAgentSlot(ctx, chatFolder)
	if err != nil {
		return "", err
	}
	defer releaseSlot()
	id, prov, releaseRuntime, err := r.turnRuntime(ctx, chatFolder, thread, tier, allowlist)
	if err != nil {
		return "", fmt.Errorf("runner: ensure %q: %w", chatFolder, err)
	}
	group = group || restrictedGroupProvider(prov)
	defer releaseRuntime()
	r.touch(chatFolder)

	// Build memory context for the system prompt via the Phase B
	// Context Compiler (policy-driven selection: pinned → recent
	// decisions → relevant preferences → facts → fallback).
	var opts RunPromptOpts
	if r.memory != nil {
		compiled := r.memory.CompileContext(ctx, chatFolder, thread, prompt)
		if compiled != "" {
			opts.AppendSystemPrompt = compiled
			r.log.Debug("injecting compiled memory context",
				slog.String("chat", chatFolder),
				slog.Int("len", len(compiled)))
		}
	}
	opts.AppendSystemPrompt = systemPromptWith(opts.AppendSystemPrompt)

	agentPrompt, err := r.handoffPrompt(ctx, chatFolder, thread, prov.Name(), prompt)
	if err != nil {
		return "", err
	}
	ref, err := r.resolveSession(ctx, id, chatFolder, thread, prov)
	if err != nil {
		return "", err
	}
	cmd := withModel(prov.RunCmd(agentPrompt, opts.AppendSystemPrompt, ref), prov, r.ModelForChat(chatFolder))
	releaseGate, err := r.admit(ctx, chatFolder, ClassInteractive)
	if err != nil {
		return "", err
	}
	if err := r.recordConversationRequest(ctx, chatFolder, thread, prompt); err != nil {
		releaseGate()
		return "", err
	}
	res, err := r.execAgentWithCancel(ctx, id, chatFolder, thread, prov, cmd, true)
	releaseGate()
	if announcesNewSession(prov, ref) {
		var stderr string
		if res != nil {
			stderr = res.Stderr
		}
		r.captureSession(ctx, chatFolder, thread, prov, stderr)
	}
	if err != nil {
		return "", err
	}
	if err := validateExecution(ctx, prov); err != nil {
		return "", err
	}
	if err := r.markConversationProvider(ctx, chatFolder, thread, prov.Name(), groupOutputText(prov, strings.TrimSpace(res.Stdout))); err != nil {
		return "", err
	}
	reply = groupOutputText(prov, strings.TrimSpace(res.Stdout))
	r.touch(chatFolder)

	// Trigger async extraction after successful agent turn.
	if r.memory != nil && r.extractFn != nil && reply != "" {
		go func() {
			bgCtx := context.Background()
			r.extractAndIngest(bgCtx, chatFolder, thread, prompt, reply)
		}()
	}

	// Auto-compact check: if turn count exceeds threshold, compact
	// the session asynchronously. Reset count IMMEDIATELY so
	// concurrent/subsequent turns don't re-trigger.
	turns := r.incrementTurnCount(chatFolder, thread)
	if r.compactAfter > 0 && turns >= r.compactAfter {
		r.resetTurnCount(chatFolder, thread) // reset BEFORE async compact
		r.log.Info("auto-compact triggered",
			slog.String("chat", chatFolder),
			slog.Int("thread", thread),
			slog.Int("turns", turns))
		go func() {
			bgCtx := context.Background()
			r.CompactSession(bgCtx, chatFolder, thread, tier, allowlist)
		}()
	}

	return reply, nil
}

// RunResult is the final outcome of a streaming agent run. Sent on
// AgentStream.Result after the exec finishes.
type RunResult struct {
	FullText string // complete stdout (trimmed)
	Stderr   string
	Err      error
}

// AgentStream is the handle callers use to consume streaming output.
// Read Chunks in a loop for partial text; when Chunks closes, read
// Result for the final outcome.
//
// Progress is nil unless the run is structured (see StructuredStreamer).
// Then it carries the agent's tool calls and Chunks carries only answer
// text. Progress is unbuffered and both channels have one sender, so a
// consumer that drains the Chunks already buffered before applying a
// progress event sees text and tools in the agent's order. A consumer
// must keep reading Progress (or cancel the run) until Chunks closes;
// Progress closes with it.
type AgentStream struct {
	Chunks   <-chan string        // answer text chunks as they arrive
	Progress <-chan ProgressEvent // tool calls of a structured run; nil otherwise
	Result   <-chan RunResult     // final result after agent exits
}

// RunStream is the streaming variant of Run. It ensures the container,
// starts the agent, and returns an AgentStream immediately. The caller
// reads from Chunks in a loop, then reads Result for the final outcome.
//
// Post-run work (extraction, auto-compact) runs in background
// goroutines, same as Run. Cancelling ctx kills the agent inside the
// container and skips that work (see startStream).
func (r *Runner) RunStream(
	ctx context.Context,
	chatFolder string,
	thread int,
	tier Tier,
	allowlist *mountsec.Allowlist,
	prompt string,
) (stream *AgentStream, retErr error) {
	_, group := r.groupRegistration(ctx, chatFolder)
	defer func() {
		if group {
			retErr = groupPublicError(retErr)
		}
	}()
	if err := r.authorizeTurn(ctx, chatFolder); err != nil {
		return nil, err
	}
	if err := r.checkGroupInput(ctx, chatFolder, prompt); err != nil {
		return nil, err
	}
	releaseSlot, err := r.acquireAgentSlot(ctx, chatFolder)
	if err != nil {
		return nil, err
	}
	transferred := false
	defer func() {
		if !transferred {
			releaseSlot()
		}
	}()
	id, prov, releaseRuntime, err := r.turnRuntime(ctx, chatFolder, thread, tier, allowlist)
	if err != nil {
		return nil, fmt.Errorf("runner: ensure %q: %w", chatFolder, err)
	}
	group = group || restrictedGroupProvider(prov)
	originalRelease := releaseSlot
	releaseSlot = func() { releaseRuntime(); originalRelease() }
	r.touch(chatFolder)

	// Build memory context (same as Run).
	var systemPrompt string
	if r.memory != nil {
		compiled := r.memory.CompileContext(ctx, chatFolder, thread, prompt)
		if compiled != "" {
			systemPrompt = compiled
			r.log.Debug("injecting compiled memory context (stream)",
				slog.String("chat", chatFolder),
				slog.Int("len", len(compiled)))
		}
	}
	systemPrompt = systemPromptWith(systemPrompt)

	agentPrompt, err := r.handoffPrompt(ctx, chatFolder, thread, prov.Name(), prompt)
	if err != nil {
		return nil, err
	}
	model := r.ModelForChat(chatFolder)
	mkCmd := func(ref SessionRef) []string {
		return withModel(prov.RunCmd(agentPrompt, systemPrompt, ref), prov, model)
	}
	transferred = true
	return r.startStream(ctx, id, convKey{chatFolder, thread}, tier, allowlist, prov, prompt, mkCmd, releaseSlot)
}

// startStream runs the agent command mkCmd builds for the
// conversation's session in container id and streams its output. It is
// RunStream after Ensure and prompt assembly.
//
// The session is resolved only once the chat's agent slot is held, and
// a new conversation's announced id is stored before the slot is
// released: after a Stop the next turn may start before this one's
// agent is dead, and it must see the session the dying turn created
// (and its transcript) rather than start a second one.
//
// Cancelling ctx stops the turn: the agent's process group is killed
// inside the container (killAgent), the stream ends, and Result carries
// an error wrapping ctx.Err(). A cancelled turn is not ingested into
// memory and does not count toward auto-compact. The chat's agent slot
// (acquireAgentSlot) is held until the process has exited or the kill
// has finished, so the next streamed turn of the chat waits for it.
func (r *Runner) startStream(
	ctx context.Context,
	id string,
	conv convKey,
	tier Tier,
	allowlist *mountsec.Allowlist,
	prov AgentProvider,
	prompt string,
	mkCmd func(SessionRef) []string,
	heldSlot ...func(),
) (stream *AgentStream, retErr error) {
	group := restrictedGroupProvider(prov)
	defer func() {
		if group {
			retErr = groupPublicError(retErr)
		}
	}()
	chatFolder := conv.folder
	var releaseSlot func()
	var err error
	if len(heldSlot) > 0 {
		releaseSlot = heldSlot[0]
	} else {
		releaseSlot, err = r.acquireAgentSlot(ctx, chatFolder)
	}
	if err != nil {
		return nil, err
	}

	started := false
	defer func() {
		if !started {
			releaseSlot()
		}
	}()

	ref, err := r.resolveSession(ctx, id, chatFolder, conv.thread, prov)
	if err != nil {
		return nil, err
	}
	cmd := mkCmd(ref)
	capture := announcesNewSession(prov, ref)
	ss := r.structuredStreamer(prov) // nil: plain text
	if ss != nil {
		cmd = ss.StructuredCmd(cmd)
	}

	// Acquire order: queue slot -> chat's agent slot -> gate (gate.go).
	releaseGate, err := r.admit(ctx, chatFolder, ClassInteractive)
	if err != nil {
		return nil, err
	}

	// Restore config from backup if needed.
	if restoreCmd := prov.RestoreConfigCmd(); restoreCmd != nil {
		r.ex.Exec(ctx, id, restoreCmd, nil)
	}

	pidFile, err := newAgentPidFile()
	if err != nil {
		releaseGate()
		return nil, err
	}
	env, release := r.agentExecEnv(chatFolder, conv.thread, prov, true)
	if err := r.recordConversationRequest(ctx, chatFolder, conv.thread, prompt); err != nil {
		release()
		releaseGate()
		return nil, err
	}
	handle, err := r.ex.ExecStream(ctx, id, wrapWithPidFile(pidFile, cmd), env)
	if err != nil {
		release()
		releaseGate()
		return nil, fmt.Errorf("runner: exec stream: %w", err)
	}

	chunks := make(chan string, 32)
	result := make(chan RunResult, 1)
	var progress chan ProgressEvent
	if ss != nil {
		progress = make(chan ProgressEvent) // unbuffered: see AgentStream
	}

	// The watcher kills the agent when the turn is cancelled. finished
	// tells it the process exited on its own; killed reports which of
	// the two happened, once the kill (if any) is over.
	finished := make(chan struct{})
	killed := make(chan bool, 1)
	started = true
	go func() {
		select {
		case <-ctx.Done():
			r.killAgent(ctx, id, chatFolder, pidFile)
			handle.Close() // ends the read loop even if the kill failed
			killed <- true
		case <-finished:
			killed <- false
		}
	}()

	go func() {
		defer close(chunks)
		defer close(result)
		if progress != nil {
			defer close(progress)
		}
		defer handle.Close()
		defer releaseSlot()
		defer release() // capability dies with the exec, not the handler

		// After a cancel nobody may be reading Chunks any more; drop
		// the tail instead of blocking the goroutine forever.
		emit := func(s string) {
			if validateExecution(ctx, prov) != nil {
				return
			}
			select {
			case chunks <- s:
			case <-ctx.Done():
			}
		}
		var filter groupChunkFilter
		send := func(s string) {
			if group {
				s = filter.Push(s)
			}
			if s != "" {
				emit(s)
			}
		}
		fullOut, authOut, stderr := r.readAgentOutput(ctx, handle, ss, send, progress, group)
		if group {
			if tail := filter.Flush(); tail != "" {
				emit(tail)
			}
		}
		stderr = strings.TrimSpace(stderr)

		// Wait for exit.
		var execErr error
		if err := <-handle.Done; err != nil {
			execErr = err
		}
		close(finished)
		if !<-killed {
			r.removePidFile(ctx, id, pidFile)
		}
		releaseGate() // the agent process is gone (exited or killed)
		if capture {
			r.captureSession(ctx, chatFolder, conv.thread, prov, stderr)
		}

		r.touch(chatFolder)
		fullText := groupOutputText(prov, strings.TrimSpace(fullOut))
		publicStderr := stderr
		if group {
			publicStderr = ""
		}

		// A cancelled turn ends here: no reply, no memory ingestion,
		// no turn count. The partial text belongs to the caller.
		if ctx.Err() != nil {
			result <- RunResult{
				Err:    outwardProviderError(prov, fmt.Errorf("runner: agent run cancelled: %w", ctx.Err())),
				Stderr: publicStderr,
			}
			return
		}

		// Classify provider quota/auth/exit errors.
		if execErr != nil {
			if ee, ok := execErr.(*ExecError); ok && ee.Code != 0 {
				if isUsageLimit(stderr, authOut) {
					result <- RunResult{Err: &UsageLimitError{Provider: prov.Name()}}
					return
				}
				if prov.IsNotLoggedInError(stderr, authOut) {
					result <- RunResult{Err: r.classifyAuthError(ctx, id, prov)}
					return
				}
				result <- RunResult{
					Err:    outwardProviderError(prov, fmt.Errorf("runner: agent exited %d (stderr=%q)", ee.Code, tailString(stderr, 600))),
					Stderr: publicStderr,
				}
				return
			}
			if group {
				result <- RunResult{Err: groupPublicError(execErr)}
				return
			}
		}

		if err := validateExecution(ctx, prov); err != nil {
			result <- RunResult{Err: outwardProviderError(prov, err)}
			return
		}
		if err := r.markConversationProvider(ctx, chatFolder, conv.thread, prov.Name(), fullText); err != nil {
			result <- RunResult{Err: outwardProviderError(prov, err)}
			return
		}
		result <- RunResult{FullText: fullText, Stderr: publicStderr}

		// Post-run: extraction, auto-compact (same as Run).
		if r.memory != nil && r.extractFn != nil && fullText != "" {
			go r.extractAndIngest(context.Background(), chatFolder, conv.thread, prompt, fullText)
		}
		turns := r.incrementTurnCount(chatFolder, conv.thread)
		if r.compactAfter > 0 && turns >= r.compactAfter {
			r.resetTurnCount(chatFolder, conv.thread)
			r.log.Info("auto-compact triggered (stream)",
				slog.String("chat", chatFolder),
				slog.Int("thread", conv.thread),
				slog.Int("turns", turns))
			go r.CompactSession(context.Background(), chatFolder, conv.thread, tier, allowlist)
		}
	}()

	return &AgentStream{Chunks: chunks, Progress: progress, Result: result}, nil
}

// structuredStreamer returns prov's structured-output capability, or
// nil when progress is off or prov has none.
func (r *Runner) structuredStreamer(prov AgentProvider) StructuredStreamer {
	if !r.progress {
		return nil
	}
	ss, _ := prov.(StructuredStreamer)
	return ss
}

// readAgentOutput reads a streamed agent run to EOF. Plain runs (ss
// nil) pass stdout through as chunks. Structured runs pass answer text
// to send and tool calls to progress, and the reply is the run's result
// text. Returns the reply, the text to search for auth errors and
// stderr. For structured runs that text is the reply only (result or
// final agent text), never tool results or the raw transcript: a file
// the agent read may well say "not logged in".
func (r *Runner) readAgentOutput(ctx context.Context, handle *StreamHandle, ss StructuredStreamer, send func(string), progress chan<- ProgressEvent, group bool) (reply, authText, stderr string) {
	if ss == nil {
		out, errOut := readExecStream(handle.Stdout, handle.Stderr, send)
		return out, out, errOut
	}
	onTool := func(ev ProgressEvent) {
		if group {
			ev.Tool = redactGroupCapabilities(ev.Tool)
			ev.Detail = redactGroupCapabilities(ev.Detail)
		}
		select {
		case progress <- ev:
		case <-ctx.Done():
		}
	}
	out := readStructuredStream(handle.Stdout, handle.Stderr, ss.ParseStreamLine, send, onTool)
	r.log.Debug("structured agent run read",
		slog.String("agent_session", out.SessionID),
		slog.Int("parsed", out.Parsed))
	if ctx.Err() == nil && (out.Malformed > 0 || !out.Result) {
		// Counts only: lines may carry file contents or secrets.
		r.log.Warn("structured agent output incomplete",
			slog.Int("parsed", out.Parsed),
			slog.Int("malformed", out.Malformed),
			slog.Bool("result", out.Result))
	}
	return out.Text, out.Text, out.Stderr
}

// SetProgress turns structured streaming (tool progress) on or off
// for providers that support it. Off by default; main wires
// config.ProgressEnabled.
func (r *Runner) SetProgress(on bool) { r.progress = on }

// readExecStream reads an exec's demuxed stdout to EOF, passing each
// chunk to onChunk, and returns the full stdout and stderr text.
//
// Stderr is drained concurrently. ExecStream demuxes both streams from
// a single goroutine into unbuffered pipes, so leaving stderr unread
// until stdout hits EOF deadlocks the first time the agent writes to
// stderr — codex exec logs its progress there, and the chat hung on
// "typing..." forever.
func readExecStream(stdout, stderr io.Reader, onChunk func(string)) (string, string) {
	stderrCh := make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(stderr)
		stderrCh <- string(b)
	}()

	var full strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := stdout.Read(buf)
		if n > 0 {
			text := string(buf[:n])
			full.WriteString(text)
			onChunk(text)
		}
		if readErr != nil {
			break // EOF or error
		}
	}
	return full.String(), <-stderrCh
}

// RunFresh is like Run but without session continuity. For system
// prompts that should not pollute conversation history.
func (r *Runner) RunFresh(
	ctx context.Context,
	chatFolder string,
	tier Tier,
	allowlist *mountsec.Allowlist,
	prompt string,
) (reply string, retErr error) {
	_, group := r.groupRegistration(ctx, chatFolder)
	defer func() {
		if group {
			reply = redactGroupCapabilities(reply)
			retErr = groupPublicError(retErr)
		}
	}()
	if err := r.authorizeTurn(ctx, chatFolder); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, freshRunTimeout)
	defer cancel()
	if err := r.checkGroupInput(ctx, chatFolder, prompt); err != nil {
		return "", err
	}
	releaseSlot, err := r.acquireBackgroundSlot(ctx, chatFolder, cancel)
	if err != nil {
		return "", err
	}
	defer releaseSlot()
	id, prov, releaseRuntime, err := r.turnRuntime(ctx, chatFolder, 0, tier, allowlist)
	if err != nil {
		return "", fmt.Errorf("runner: ensure %q: %w", chatFolder, err)
	}
	group = group || restrictedGroupProvider(prov)
	defer releaseRuntime()
	releaseGate, err := r.admit(ctx, chatFolder, ClassBackground)
	if err != nil {
		return "", err
	}
	res, err := r.execAgentWithCancel(ctx, id, chatFolder, 0, prov, prov.RunFreshCmd(prompt), false)
	releaseGate()
	if err != nil {
		return "", err
	}
	if err := validateExecution(ctx, prov); err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Stdout), nil
}

// execAgent runs a provider-built command inside a container with
// config restore and error classification. On a non-zero exit it
// returns the classified error together with the result, so callers can
// still read what the agent printed (a new conversation's session id).
// thread is the conversation the memory capability is bound to (0 for
// system runs). interactive is true for every turn that goes through Run or
// RunStream, which includes the scheduled silent turns of the owner chat
// (telegram.runTaskSilently calls Run), and false for system runs (RunFresh).
// Only interactive execs may receive an artifact delivery capability; it can
// deliver only files under the owner's own workspace to the owner's own chat.
func (r *Runner) execAgent(ctx context.Context, containerID, chatFolder string, thread int, prov AgentProvider, cmd []string, interactive bool) (*ExecResult, error) {
	// Restore config from backup if needed.
	if restoreCmd := prov.RestoreConfigCmd(); restoreCmd != nil {
		r.ex.Exec(ctx, containerID, restoreCmd, nil)
	}

	env, release := r.agentExecEnv(chatFolder, thread, prov, interactive)
	defer release()
	res, err := r.ex.Exec(ctx, containerID, cmd, env)
	if err != nil {
		return nil, outwardProviderError(prov, fmt.Errorf("runner: exec: %w", err))
	}
	if res.ExitCode != 0 {
		stderr := strings.TrimSpace(res.Stderr)
		if isUsageLimit(stderr, res.Stdout) {
			return res, &UsageLimitError{Provider: prov.Name()}
		}
		if prov.IsNotLoggedInError(stderr, res.Stdout) {
			return res, r.classifyAuthError(ctx, containerID, prov)
		}
		return res, outwardProviderError(prov, fmt.Errorf("runner: agent exited %d (stderr=%q)", res.ExitCode, stderr))
	}
	return res, nil
}

// classifyAuthError distinguishes "never logged in" from "session expired".
func (r *Runner) classifyAuthError(ctx context.Context, containerID string, prov AgentProvider) error {
	status, err := r.checkAuthViaProvider(ctx, containerID, prov)
	if err != nil {
		return ErrNotLoggedIn
	}
	if status.AuthMethod != "" && status.AuthMethod != "none" {
		return ErrSessionExpired
	}
	return ErrNotLoggedIn
}

func (r *Runner) checkAuthViaProvider(ctx context.Context, containerID string, prov AgentProvider) (AuthStatus, error) {
	checkCtx, cancel := context.WithTimeout(ctx, startupExecTimeout)
	defer cancel()
	res, err := r.execLocalProbe(checkCtx, containerID, prov.AuthStatusCmd())
	if err != nil {
		if errors.Is(err, errLocalProbeCleanup) {
			return AuthStatus{}, errLocalProbeCleanup
		}
		if checkCtx.Err() != nil {
			return AuthStatus{}, checkCtx.Err()
		}
		return AuthStatus{}, fmt.Errorf("runner: auth status unavailable")
	}
	output := res.Stdout
	if prov.Name() == "codex" {
		output += "\n" + res.Stderr
	}
	if strings.TrimSpace(output) == "" {
		return AuthStatus{}, fmt.Errorf("runner: auth status empty")
	}
	status, err := prov.ParseAuthStatus(output)
	if err != nil {
		return AuthStatus{}, fmt.Errorf("runner: auth status invalid")
	}
	if res.ExitCode != 0 && status.LoggedIn {
		return AuthStatus{}, fmt.Errorf("runner: auth status failed")
	}
	return status, nil
}

// RestoreChatProvider only restores trusted persisted selection before dispatch.
// It never removes a container, writes state or resets the saved model.
func (r *Runner) RestoreChatProvider(chatFolder string, p AgentProvider) {
	r.setChatProviderOverride(chatFolder, p)
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

// SetChatProvider restores an override during boot and removes an old image.
// Runtime commands must use SwitchChatProvider for persistence and serialization.
func (r *Runner) SetChatProvider(chatFolder string, p AgentProvider) {
	r.setChatProviderOverride(chatFolder, p)

	if r.d == nil {
		return
	} // subprocess/test runner has no Docker container
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

func (r *Runner) setChatProviderOverride(chatFolder string, p AgentProvider) {
	r.providersMu.Lock()
	if r.chatProviders == nil {
		r.chatProviders = make(map[string]AgentProvider)
	}
	r.chatProviders[chatFolder] = p
	r.invalidateStartupLocked(chatFolder)
	delete(r.selectionBlocks, chatFolder)
	r.providersMu.Unlock()

}

// ClearChatProvider removes a per-chat override (reverts to default).
func (r *Runner) ClearChatProvider(chatFolder string) {
	r.providersMu.Lock()
	delete(r.chatProviders, chatFolder)
	r.invalidateStartupLocked(chatFolder)
	r.providersMu.Unlock()
}

// CheckAuth probes auth state of the chat's agent container.
func (r *Runner) CheckAuth(ctx context.Context, chatFolder string, tier Tier, allowlist *mountsec.Allowlist) (AuthStatus, error) {
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return AuthStatus{}, err
	}
	release, err := r.acquireAgentSlot(ctx, chatFolder)
	if err != nil {
		return AuthStatus{}, err
	}
	defer release()
	id, err := r.ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return AuthStatus{}, err
	}
	return r.checkAuthViaProvider(ctx, id, r.ProviderForChat(chatFolder))
}

// stateStore is the interface Runner needs from the store: compact
// timestamps and the session-adoption marker in router_state, and one
// agent session id per conversation (see session.go). Consumer-side
// interface.
type stateStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
	GetSession(ctx context.Context, folder string, thread int, provider string) (string, bool, error)
	PutSession(ctx context.Context, folder string, thread int, provider, id string) error
	DeleteSession(ctx context.Context, folder string, thread int, provider string) error
}

// SetStore wires the store. Without one, conversations are not kept
// apart: every turn uses the implicit SessionRef (legacy behaviour).
func (r *Runner) SetStore(s stateStore) { r.store = s }

// MemoryCompiler is the interface the Runner needs from the memory
// layer. CompileContext returns the formatted system prompt appendix
// using the Phase B policy-driven selection (kind-based budgets,
// pinned items, recency weighting).
type MemoryCompiler interface {
	CompileContext(ctx context.Context, chatFolder string, thread int, query string) string
}

// SetMemory wires the memory layer after construction.
func (r *Runner) SetMemory(m MemoryCompiler) { r.memory = m }

// SetExtractFn wires the conversation extraction callback. It gets the
// turn's conversation (chatFolder, thread) so extracted facts are stored
// in that thread (D023).
func (r *Runner) SetExtractFn(fn func(ctx context.Context, chatFolder string, thread int, userMsg, agentReply string)) {
	r.extractFn = fn
}

// SetCompactFn wires the session compact callback (summarize → store →
// clear) for one conversation (chatFolder, thread).
func (r *Runner) SetCompactFn(fn func(ctx context.Context, chatFolder string, thread int) error) {
	r.compactFn = fn
}

// SetCompactAfter sets the turn threshold for auto-compact. 0 = disabled.
func (r *Runner) SetCompactAfter(n int) { r.compactAfter = n }

// ClearSession invalidates this conversation across providers and clears its
// neutral transcript. Other threads, shared memory and files remain. With a
// legacy store lacking continuity support it deletes only the current native
// session as before. Old native files are retained but never resumed by the
// continuity-aware path.
func (r *Runner) ClearSession(ctx context.Context, chatFolder string, thread int, tier Tier, allowlist *mountsec.Allowlist) error {
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return err
	}
	release, err := r.acquireAgentSlot(ctx, chatFolder)
	if err != nil {
		return err
	}
	defer release()
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return err
	}
	if s, ok := r.store.(continuityStore); ok {
		if err := s.ClearConversationContinuity(ctx, chatFolder, thread); err != nil {
			return fmt.Errorf("runner: clearing conversation continuity: %w", err)
		}
		r.resetTurnCount(chatFolder, thread)
		return nil
	}
	defer r.resetTurnCount(chatFolder, thread)
	prov := r.ProviderForChat(chatFolder)
	if r.store == nil {
		// No conversation ids without a store: legacy wipe of the
		// chat's sessions, as before conversations existed.
		id, err := r.ensure(ctx, chatFolder, tier, allowlist)
		if err != nil {
			return err
		}
		_, err = r.ex.Exec(ctx, id, prov.ClearSessionCmd(SessionRef{}), nil)
		return err
	}
	need, err := r.needsClear(ctx, chatFolder, thread, prov)
	if err != nil || !need {
		return err
	}
	id, err := r.ensure(ctx, chatFolder, tier, allowlist)
	if err != nil {
		return err
	}
	return r.clearConversation(ctx, id, chatFolder, thread, prov)
}

// CompactSession summarizes the current conversation, stores the
// summary in memory, then clears the session. The summary becomes
// the bridge between the old conversation and the new one — the
// Context Compiler injects it via the reserved summary slot.
func (r *Runner) CompactSession(ctx context.Context, chatFolder string, thread int, tier Tier, allowlist *mountsec.Allowlist) (string, error) {
	if err := r.authorizeProfile(ctx, chatFolder); err != nil {
		return "", err
	}
	if _, group := r.groupRegistration(ctx, chatFolder); group {
		return "", ErrGroupMaintenanceUnavailable
	}
	if r.compactFn == nil {
		return "", r.ClearSession(ctx, chatFolder, thread, tier, allowlist)
	}
	if r.getTurnCount(chatFolder, thread) == 0 {
		return "nothing to compact (0 turns)", nil
	}
	if err := r.compactFn(ctx, chatFolder, thread); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return "", fmt.Errorf("runner: compaction interrupted; context preserved: %w", err)
		}
		r.log.Warn("compact summarize failed, clearing anyway",
			slog.String("chat", chatFolder), slog.Int("thread", thread), slog.Any("err", err))
	}
	if err := r.ClearSession(ctx, chatFolder, thread, tier, allowlist); err != nil {
		return "", err
	}
	return "compacted", nil
}

// convKey names one conversation: a chat folder and a Telegram thread
// (0 = the main chat). Containers stay per chat; sessions, turn
// counters and compaction are per conversation.
type convKey struct {
	folder string
	thread int
}

func (r *Runner) incrementTurnCount(chatFolder string, thread int) int {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	k := convKey{chatFolder, thread}
	r.turnCount[k]++
	return r.turnCount[k]
}

func (r *Runner) getTurnCount(chatFolder string, thread int) int {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	return r.turnCount[convKey{chatFolder, thread}]
}

func (r *Runner) resetTurnCount(chatFolder string, thread int) {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	r.turnCount[convKey{chatFolder, thread}] = 0
}

// activeConversations returns the conversations with at least one
// turn since their last compact or clear.
func (r *Runner) activeConversations() []convKey {
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	out := make([]convKey, 0, len(r.turnCount))
	for k, n := range r.turnCount {
		if n > 0 {
			out = append(out, k)
		}
	}
	return out
}

// extractAndIngest is the background callback for Phase A. Runs
// after each successful agent turn — extracts structured facts and
// stores them in memory under the turn's conversation.
func (r *Runner) extractAndIngest(ctx context.Context, chatFolder string, thread int, userMsg, agentReply string) {
	if r.extractFn != nil {
		r.extractFn(ctx, chatFolder, thread, userMsg, agentReply)
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
// at least once in this gopod process. Used by the idle watcher
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
// indirection lets us serialise Ensure calls and session bookkeeping
// (session.go) for the same chat without holding a global lock that
// would serialise across chats. Never held across an agent run.
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

// BlockChatSelection prevents dispatch through an unresolved persisted selection.
func (r *Runner) BlockChatSelection(folder string) {
	r.providersMu.Lock()
	defer r.providersMu.Unlock()
	if r.selectionBlocks == nil {
		r.selectionBlocks = make(map[string]bool)
	}
	r.selectionBlocks[folder] = true
	r.invalidateStartupLocked(folder)
}
func (r *Runner) selectionBlocked(folder string) bool {
	r.providersMu.RLock()
	defer r.providersMu.RUnlock()
	return r.selectionBlocks[folder]
}
