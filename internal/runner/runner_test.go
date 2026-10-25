package runner

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/google/uuid"
)

// --- fakes -------------------------------------------------------------

// fakeSessionStore is the store slice the runner uses, in memory.
type fakeSessionStore struct {
	mu       sync.Mutex
	sessions map[string]string // "folder/thread/provider" -> id
	state    map[string]string
}

func newFakeSessionStore() *fakeSessionStore {
	return &fakeSessionStore{sessions: map[string]string{}, state: map[string]string{}}
}

func sessKey(folder string, thread int, provider string) string {
	return fmt.Sprintf("%s/%d/%s", folder, thread, provider)
}

func (s *fakeSessionStore) GetState(_ context.Context, key string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state[key], nil
}

func (s *fakeSessionStore) SetState(_ context.Context, key, value string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state[key] = value
	return nil
}

func (s *fakeSessionStore) GetSession(_ context.Context, folder string, thread int, provider string) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.sessions[sessKey(folder, thread, provider)]
	return id, ok, nil
}

func (s *fakeSessionStore) PutSession(_ context.Context, folder string, thread int, provider, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sessKey(folder, thread, provider)] = id
	return nil
}

func (s *fakeSessionStore) DeleteSession(_ context.Context, folder string, thread int, provider string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, sessKey(folder, thread, provider))
	return nil
}

func (s *fakeSessionStore) row(folder string, thread int, provider string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.sessions[sessKey(folder, thread, provider)]
}

// fakeAgentHome simulates one chat container: the claude and codex CLIs
// and the session transcripts in their home directories. It answers the
// provider's helper commands (latest id, exists, clear, restore) and
// runs the agent commands with the CLIs' session semantics.
type fakeAgentHome struct {
	t     *testing.T
	provs []AgentProvider

	mu         sync.Mutex
	sessions   map[string][]string // provider -> ids, least recently touched first
	runs       [][]string          // agent argvs, unwrapped
	latestRuns int
	execs      int // every exec, helper or agent

	// failNextClaudeNew makes the next `claude --session-id` exit 1
	// before writing a transcript (a first turn that died early).
	failNextClaudeNew bool
	// failWritesTranscript: the failing first turn did write one.
	failWritesTranscript bool
	// codexQuiet makes a new codex conversation print no session id.
	codexQuiet bool
	// codexStdoutDecoy / codexStderrDecoy: a `session id:` line naming
	// another session that a new codex conversation prints in its reply
	// (stdout) or later in its stderr log, after the real header.
	codexStdoutDecoy string
	codexStderrDecoy string
}

func newFakeAgentHome(t *testing.T) *fakeAgentHome {
	return &fakeAgentHome{
		t:        t,
		provs:    providersUnderTest(),
		sessions: map[string][]string{},
	}
}

// seed creates a pre-existing (legacy) session for provider.
func (f *fakeAgentHome) seed(provider, id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.touchLocked(provider, id)
}

func (f *fakeAgentHome) has(provider, id string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Contains(f.sessions[provider], id)
}

func (f *fakeAgentHome) agentRuns() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.runs...)
}

func (f *fakeAgentHome) lastRun() []string {
	runs := f.agentRuns()
	if len(runs) == 0 {
		f.t.Fatal("no agent run")
	}
	return runs[len(runs)-1]
}

func (f *fakeAgentHome) touchLocked(provider, id string) {
	ids := slices.DeleteFunc(f.sessions[provider], func(s string) bool { return s == id })
	f.sessions[provider] = append(ids, id)
}

func (f *fakeAgentHome) Exec(_ context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	if len(cmd) > 4 && cmd[0] == "sh" && strings.Contains(cmd[2], `echo $$ >`) {
		cmd = cmd[4:]
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.execs++
	if cmd[0] == "claude" || cmd[0] == "codex" {
		return f.agentLocked(cmd), nil
	}
	if isRmCmd(cmd) {
		return &ExecResult{}, nil
	}
	last := cmd[len(cmd)-1]
	for _, p := range f.provs {
		name := p.Name()
		switch {
		case reflect.DeepEqual(cmd, p.RestoreConfigCmd()):
			return &ExecResult{}, nil
		case reflect.DeepEqual(cmd, p.LatestSessionIDCmd()):
			f.latestRuns++
			ids := f.sessions[name]
			if len(ids) == 0 {
				return &ExecResult{}, nil
			}
			return &ExecResult{Stdout: ids[len(ids)-1] + "\n"}, nil
		case reflect.DeepEqual(cmd, p.SessionExistsCmd(last)):
			if slices.Contains(f.sessions[name], last) {
				return &ExecResult{}, nil
			}
			return &ExecResult{ExitCode: 1}, nil
		case reflect.DeepEqual(cmd, p.ClearSessionCmd(SessionRef{ID: last})):
			f.sessions[name] = slices.DeleteFunc(f.sessions[name], func(s string) bool { return s == last })
			return &ExecResult{}, nil
		}
	}
	f.t.Errorf("unexpected exec %q", cmd)
	return &ExecResult{ExitCode: 127}, nil
}

// ExecStream runs the (pid-file wrapped) agent command to completion and
// replays its output through pipes.
func (f *fakeAgentHome) ExecStream(_ context.Context, _ string, cmd, _ []string) (*StreamHandle, error) {
	if len(cmd) < 5 || cmd[0] != "sh" || cmd[2] != `echo $$ > "$0"; exec "$@"` {
		f.t.Fatalf("stream cmd not wrapped: %q", cmd)
	}
	f.mu.Lock()
	f.execs++
	res := f.agentLocked(cmd[4:])
	f.mu.Unlock()

	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	done := make(chan error, 1)
	go func() {
		var wg sync.WaitGroup
		wg.Add(2)
		go func() { defer wg.Done(); io.WriteString(outW, res.Stdout); outW.Close() }()
		go func() { defer wg.Done(); io.WriteString(errW, res.Stderr); errW.Close() }()
		wg.Wait()
		if res.ExitCode != 0 {
			done <- &ExecError{Code: res.ExitCode}
		}
		close(done)
	}()
	return &StreamHandle{Stdout: outR, Stderr: errR, Done: done, cancel: func() {}}, nil
}

func flagValue(cmd []string, flag string) (string, bool) {
	i := slices.Index(cmd, flag)
	if i < 0 || i+1 >= len(cmd) {
		return "", false
	}
	return cmd[i+1], true
}

func (f *fakeAgentHome) agentLocked(cmd []string) *ExecResult {
	f.runs = append(f.runs, cmd)
	switch cmd[0] {
	case "claude":
		if id, ok := flagValue(cmd, "--session-id"); ok {
			if slices.Contains(f.sessions["claude"], id) {
				return &ExecResult{ExitCode: 1, Stderr: "Error: Session ID " + id + " is already in use."}
			}
			if f.failNextClaudeNew {
				f.failNextClaudeNew = false
				if f.failWritesTranscript {
					f.touchLocked("claude", id)
				}
				return &ExecResult{ExitCode: 1, Stderr: "API Error: overloaded"}
			}
			f.touchLocked("claude", id)
			return &ExecResult{Stdout: "reply\n"}
		}
		if id, ok := flagValue(cmd, "--resume"); ok {
			if !slices.Contains(f.sessions["claude"], id) {
				return &ExecResult{ExitCode: 1, Stderr: "No conversation found with session ID: " + id}
			}
			f.touchLocked("claude", id)
			return &ExecResult{Stdout: "reply\n"}
		}
		f.t.Errorf("claude run without explicit session: %q", cmd)
		return &ExecResult{Stdout: "reply\n"}
	case "codex":
		if len(cmd) > 2 && cmd[1] == "exec" && cmd[2] == "resume" {
			id := cmd[3]
			if id == "--last" {
				f.t.Errorf("codex run without explicit session: %q", cmd)
				return &ExecResult{Stdout: "reply\n"}
			}
			if !slices.Contains(f.sessions["codex"], id) {
				return &ExecResult{ExitCode: 1, Stderr: "Error: no rollout found for thread id " + id}
			}
			f.touchLocked("codex", id)
			return &ExecResult{Stdout: "reply\n", Stderr: "session id: " + id + "\n"}
		}
		id := uuid.NewString()
		f.touchLocked("codex", id)
		res := &ExecResult{Stdout: "reply\n"}
		if f.codexStdoutDecoy != "" {
			res.Stdout = "session id: " + f.codexStdoutDecoy + "\nreply\n"
		}
		if !f.codexQuiet {
			res.Stderr = "OpenAI Codex v0.164.0\n--------\nsession id: " + id + "\n--------\n"
		}
		if f.codexStderrDecoy != "" {
			res.Stderr += "exec ls ~/.codex/sessions\nsession id: " + f.codexStderrDecoy + "\n"
		}
		return res
	}
	return &ExecResult{ExitCode: 127}
}

// --- harness -----------------------------------------------------------

const convChat = "chat"

type convHarness struct {
	r       *Runner
	home    *fakeAgentHome
	store   *fakeSessionStore
	ensures int
	mu      sync.Mutex
}

func newConvHarness(t *testing.T, prov AgentProvider) *convHarness {
	t.Helper()
	h := &convHarness{home: newFakeAgentHome(t), store: newFakeSessionStore()}
	h.r = &Runner{
		ex:             h.home,
		provider:       prov,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		turnCount:      make(map[convKey]int),
		activity:       make(map[string]time.Time),
		memory:         nopMemory{},
		store:          h.store,
		containerLocks: make(map[string]*sync.Mutex),
	}
	h.r.ensureFn = func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
		h.mu.Lock()
		h.ensures++
		h.mu.Unlock()
		return "cid", nil
	}
	return h
}

func (h *convHarness) run(t *testing.T, thread int) {
	t.Helper()
	if _, err := h.r.Run(context.Background(), convChat, thread, TierRegistered, nil, "hi"); err != nil {
		t.Fatalf("Run(thread %d): %v", thread, err)
	}
}

func (h *convHarness) runStream(t *testing.T, thread int) RunResult {
	t.Helper()
	stream, err := h.r.RunStream(context.Background(), convChat, thread, TierRegistered, nil, "hi")
	if err != nil {
		t.Fatalf("RunStream(thread %d): %v", thread, err)
	}
	return readResult(t, stream)
}

func (h *convHarness) clear(t *testing.T, thread int) {
	t.Helper()
	if err := h.r.ClearSession(context.Background(), convChat, thread, TierRegistered, nil); err != nil {
		t.Fatalf("ClearSession(thread %d): %v", thread, err)
	}
}

func (h *convHarness) row(thread int) string {
	return h.store.row(convChat, thread, h.r.ProviderForChat(convChat).Name())
}

// sessionOf returns the session an agent argv runs in and whether it
// resumes it. A new codex conversation has no id on the command line.
func sessionOf(t *testing.T, cmd []string) (id string, resume bool) {
	t.Helper()
	switch cmd[0] {
	case "claude":
		if id, ok := flagValue(cmd, "--resume"); ok {
			return id, true
		}
		id, _ := flagValue(cmd, "--session-id")
		return id, false
	case "codex":
		if cmd[2] == "resume" {
			return cmd[3], true
		}
		return "", false
	}
	t.Fatalf("not an agent cmd: %q", cmd)
	return "", false
}

func wantResume(t *testing.T, cmd []string, id string) {
	t.Helper()
	got, resume := sessionOf(t, cmd)
	if !resume || got != id {
		t.Fatalf("agent ran %q; want resume of %s", cmd, id)
	}
}

func wantNew(t *testing.T, cmd []string) {
	t.Helper()
	if _, resume := sessionOf(t, cmd); resume {
		t.Fatalf("agent ran %q; want a new conversation", cmd)
	}
}

const (
	legacySess = "0f0f0f0f-1111-4222-8333-444444444444"
	thread7    = 7
	thread9    = 9
)

// --- tests -------------------------------------------------------------

func TestMainAdoptsLatestSessionOnce(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name()+" legacy session", func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)

			h.run(t, 0)
			wantResume(t, h.home.lastRun(), legacySess)
			h.run(t, 0)
			wantResume(t, h.home.lastRun(), legacySess)

			if h.home.latestRuns != 1 {
				t.Errorf("LatestSessionIDCmd ran %d times, want 1", h.home.latestRuns)
			}
			if got := h.row(0); got != legacySess {
				t.Errorf("main row = %q, want %q", got, legacySess)
			}
		})
		t.Run(prov.Name()+" empty home", func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.run(t, 0)
			wantNew(t, h.home.lastRun())
			first := h.row(0)
			if !validSessionID(first) {
				t.Fatalf("main row after first turn = %q", first)
			}
			h.run(t, 0)
			wantResume(t, h.home.lastRun(), first)
			if h.home.latestRuns != 1 {
				t.Errorf("LatestSessionIDCmd ran %d times, want 1", h.home.latestRuns)
			}
		})
	}
}

func TestThreadGetsOwnSessionAndMainIsUntouched(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)

			// The thread speaks first after the upgrade: main's legacy
			// session must still be the one main resumes, although the
			// thread's is newer by then.
			h.run(t, thread7)
			wantNew(t, h.home.lastRun())
			th := h.row(thread7)
			if !validSessionID(th) || th == legacySess {
				t.Fatalf("thread row = %q", th)
			}
			if !h.home.has(prov.Name(), th) {
				t.Fatalf("thread row %s names no session the CLI created", th)
			}

			h.run(t, 0)
			wantResume(t, h.home.lastRun(), legacySess)
			h.run(t, thread7)
			wantResume(t, h.home.lastRun(), th)

			// A second thread is separate from both.
			h.run(t, thread9)
			wantNew(t, h.home.lastRun())
			if other := h.row(thread9); other == th || other == legacySess {
				t.Fatalf("thread 9 shares a session: %q", other)
			}
			h.run(t, 0)
			wantResume(t, h.home.lastRun(), legacySess)
			if h.home.latestRuns != 1 {
				t.Errorf("LatestSessionIDCmd ran %d times, want 1", h.home.latestRuns)
			}
		})
	}
}

func TestClearThreadDoesNotTouchMain(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)
			h.run(t, 0)
			h.run(t, thread7)
			th := h.row(thread7)
			h.run(t, thread7) // a turn to count

			h.clear(t, thread7)
			if h.row(thread7) != "" {
				t.Error("thread row survived its /clear")
			}
			if h.home.has(prov.Name(), th) {
				t.Error("thread transcript survived its /clear")
			}
			if h.row(0) != legacySess || !h.home.has(prov.Name(), legacySess) {
				t.Error("clearing the thread touched main")
			}
			if n := h.r.getTurnCount(convChat, thread7); n != 0 {
				t.Errorf("thread turn count = %d after clear", n)
			}
			if n := h.r.getTurnCount(convChat, 0); n != 1 {
				t.Errorf("main turn count = %d, want 1", n)
			}

			h.run(t, thread7)
			wantNew(t, h.home.lastRun())
			if h.row(thread7) == th {
				t.Error("thread reused its cleared session")
			}
			h.run(t, 0)
			wantResume(t, h.home.lastRun(), legacySess)
		})
	}
}

func TestClearMainDoesNotTouchThreads(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)
			h.run(t, 0)
			h.run(t, thread7)
			th := h.row(thread7)

			h.clear(t, 0)
			if h.row(0) != "" || h.home.has(prov.Name(), legacySess) {
				t.Error("main session survived its /clear")
			}
			if h.row(thread7) != th || !h.home.has(prov.Name(), th) {
				t.Error("clearing main touched the thread")
			}

			// Main starts afresh: it must not re-adopt the newest
			// transcript, which is the thread's.
			h.run(t, 0)
			wantNew(t, h.home.lastRun())
			if m := h.row(0); m == th || m == legacySess {
				t.Fatalf("main row after clear = %q", m)
			}
			h.run(t, thread7)
			wantResume(t, h.home.lastRun(), th)
			if h.home.latestRuns != 1 {
				t.Errorf("LatestSessionIDCmd ran %d times, want 1", h.home.latestRuns)
			}
		})
	}
}

func TestClearWithoutRowIsNoop(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name()+" thread", func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)
			h.clear(t, thread7)
			if h.ensures != 0 || h.home.execs != 0 {
				t.Errorf("no-op clear started the container (%d) or ran execs (%d)", h.ensures, h.home.execs)
			}
			if !h.home.has(prov.Name(), legacySess) {
				t.Error("no-op clear removed a session")
			}
		})
		t.Run(prov.Name()+" main after its own clear", func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.run(t, 0)
			h.run(t, thread7)
			th := h.row(thread7)
			h.clear(t, 0)
			execs, ensures := h.home.execs, h.ensures
			h.clear(t, 0)
			if h.home.execs != execs || h.ensures != ensures {
				t.Error("second clear of main did work")
			}
			if !h.home.has(prov.Name(), th) || h.row(thread7) != th {
				t.Error("second clear of main touched the thread")
			}
		})
	}
}

// Before main's legacy session is adopted, /clear in main means that
// session: it is adopted and cleared, and main's next turn is new.
func TestClearMainBeforeAdoptionClearsLegacySession(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newConvHarness(t, prov)
			h.home.seed(prov.Name(), legacySess)
			h.clear(t, 0)
			if h.home.has(prov.Name(), legacySess) {
				t.Fatal("legacy session survived /clear in main")
			}
			h.run(t, 0)
			wantNew(t, h.home.lastRun())
		})
	}
}

func TestProviderSwitchDoesNotResumeForeignID(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	h.run(t, 0)
	h.run(t, thread7)
	claudeMain, claudeThread := h.row(0), h.row(thread7)

	h.r.chatProviders = map[string]AgentProvider{convChat: NewCodexProvider("img")}
	from := len(h.home.agentRuns())
	h.run(t, 0)
	h.run(t, thread7)
	for _, cmd := range h.home.agentRuns()[from:] {
		if cmd[0] != "codex" {
			t.Fatalf("ran %q after switching to codex", cmd)
		}
		for _, a := range cmd {
			if a == claudeMain || a == claudeThread {
				t.Fatalf("codex got a claude session id: %q", cmd)
			}
		}
		wantNew(t, cmd)
	}
	codexMain := h.row(0)
	if !validSessionID(codexMain) || codexMain == claudeMain {
		t.Fatalf("codex main row = %q", codexMain)
	}

	// Switching back resumes claude's own sessions, untouched.
	h.r.chatProviders = nil
	h.run(t, 0)
	wantResume(t, h.home.lastRun(), claudeMain)
	h.run(t, thread7)
	wantResume(t, h.home.lastRun(), claudeThread)
}

// Claude's first turn stores the id before the run. If that turn dies
// before Claude writes a transcript, --resume would fail forever, so the
// next turn notices and starts over under a new id.
func TestClaudeFailedFirstTurnStartsOver(t *testing.T) {
	t.Run("no transcript written", func(t *testing.T) {
		h := newConvHarness(t, NewClaudeProvider("img"))
		h.home.failNextClaudeNew = true
		if _, err := h.r.Run(context.Background(), convChat, thread7, TierRegistered, nil, "hi"); err == nil {
			t.Fatal("first turn did not fail")
		}
		first := h.row(thread7)
		if !validSessionID(first) {
			t.Fatalf("row after failed first turn = %q; it must be stored before the run", first)
		}

		h.run(t, thread7)
		wantNew(t, h.home.lastRun())
		second := h.row(thread7)
		if second == first || !h.home.has("claude", second) {
			t.Fatalf("second turn: row %q (first %q)", second, first)
		}
		h.run(t, thread7)
		wantResume(t, h.home.lastRun(), second)
	})
	t.Run("transcript written", func(t *testing.T) {
		h := newConvHarness(t, NewClaudeProvider("img"))
		h.home.failNextClaudeNew = true
		h.home.failWritesTranscript = true
		if _, err := h.r.Run(context.Background(), convChat, thread7, TierRegistered, nil, "hi"); err == nil {
			t.Fatal("first turn did not fail")
		}
		first := h.row(thread7)
		// --session-id again would be "already in use"; it must resume.
		h.run(t, thread7)
		wantResume(t, h.home.lastRun(), first)
	})
	t.Run("streaming", func(t *testing.T) {
		h := newConvHarness(t, NewClaudeProvider("img"))
		h.home.failNextClaudeNew = true
		if res := h.runStream(t, thread7); res.Err == nil {
			t.Fatal("first turn did not fail")
		}
		first := h.row(thread7)
		if res := h.runStream(t, thread7); res.Err != nil {
			t.Fatalf("second turn: %v", res.Err)
		}
		wantNew(t, h.home.lastRun())
		if second := h.row(thread7); second == first || !validSessionID(second) {
			t.Fatalf("second row %q (first %q)", second, first)
		}
	})
}

func TestCodexCapturesSessionID(t *testing.T) {
	type runFn func(h *convHarness, t *testing.T, thread int)
	paths := map[string]runFn{
		"sync": func(h *convHarness, t *testing.T, thread int) { h.run(t, thread) },
		"stream": func(h *convHarness, t *testing.T, thread int) {
			if res := h.runStream(t, thread); res.Err != nil {
				t.Fatalf("stream: %v", res.Err)
			}
		},
	}
	for name, run := range paths {
		t.Run(name, func(t *testing.T) {
			h := newConvHarness(t, NewCodexProvider("img"))
			run(h, t, thread7)
			wantNew(t, h.home.lastRun())
			id := h.row(thread7)
			if !validSessionID(id) || !h.home.has("codex", id) {
				t.Fatalf("captured row = %q, want the thread id codex printed", id)
			}
			run(h, t, thread7)
			wantResume(t, h.home.lastRun(), id)
		})
		t.Run(name+" no id announced", func(t *testing.T) {
			h := newConvHarness(t, NewCodexProvider("img"))
			h.home.codexQuiet = true
			run(h, t, thread7)
			if got := h.row(thread7); got != "" {
				t.Fatalf("row = %q with nothing announced", got)
			}
			run(h, t, thread7)
			wantNew(t, h.home.lastRun())
		})
	}
}

// The streaming path resolves sessions exactly like Run.
func TestRunStreamUsesConversationSession(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	h.home.seed("claude", legacySess)
	if res := h.runStream(t, thread7); res.Err != nil || res.FullText != "reply" {
		t.Fatalf("result = %+v", res)
	}
	th := h.row(thread7)
	wantNew(t, h.home.lastRun())
	if got, _ := sessionOf(t, h.home.lastRun()); got != th {
		t.Fatalf("ran %q, stored %q", got, th)
	}
	h.runStream(t, 0)
	wantResume(t, h.home.lastRun(), legacySess)
	h.runStream(t, thread7)
	wantResume(t, h.home.lastRun(), th)
	if n := h.r.getTurnCount(convChat, thread7); n != 2 {
		t.Errorf("thread turn count = %d, want 2", n)
	}
	if n := h.r.getTurnCount(convChat, 0); n != 1 {
		t.Errorf("main turn count = %d, want 1", n)
	}
}

// A stored row whose transcript is gone (removed by hand, or a row
// carried over from the pre-thread sessions table) is dropped; for main
// that falls back to adopting the legacy session.
func TestStaleMainRowFallsBackToAdoption(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	h.home.seed("claude", legacySess)
	h.store.PutSession(context.Background(), convChat, 0, "claude", testSessA) // no transcript
	h.run(t, 0)
	wantResume(t, h.home.lastRun(), legacySess)
}

func TestCompactSummarizesAndClearsOnlyThatConversation(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	var got []convKey
	h.r.SetCompactFn(func(_ context.Context, folder string, thread int) error {
		got = append(got, convKey{folder, thread})
		return nil
	})
	h.run(t, 0)
	main := h.row(0)
	h.run(t, thread7)
	th := h.row(thread7)

	msg, err := h.r.CompactSession(context.Background(), convChat, thread7, TierRegistered, nil)
	if err != nil || msg != "compacted" {
		t.Fatalf("compact = %q, %v", msg, err)
	}
	if !reflect.DeepEqual(got, []convKey{{convChat, thread7}}) {
		t.Errorf("compactFn calls = %v", got)
	}
	if h.row(thread7) != "" || h.home.has("claude", th) {
		t.Error("compacted thread kept its session")
	}
	if h.row(0) != main || !h.home.has("claude", main) {
		t.Error("compacting the thread touched main")
	}
	if msg, _ := h.r.CompactSession(context.Background(), convChat, thread7, TierRegistered, nil); !strings.HasPrefix(msg, "nothing to compact") {
		t.Errorf("second compact = %q", msg)
	}
}

func TestNoStoreKeepsImplicitSession(t *testing.T) {
	h := newConvHarness(t, NewClaudeProvider("img"))
	h.r.store = nil
	ref, err := h.r.resolveSession(context.Background(), "cid", convChat, thread7, h.r.provider)
	if err != nil || ref != (SessionRef{}) {
		t.Fatalf("ref = %+v, %v; want the zero ref", ref, err)
	}
}

// --- fix round 1 ---------------------------------------------------------

// The session id of a new Codex conversation comes from the stderr
// header only: a `session id:` line in the reply (stdout) or later in
// the stderr log must never pick the session this conversation resumes.
func TestCodexCaptureIgnoresDecoys(t *testing.T) {
	type runFn func(h *convHarness, t *testing.T)
	paths := map[string]runFn{
		"sync": func(h *convHarness, t *testing.T) { h.run(t, thread7) },
		"stream": func(h *convHarness, t *testing.T) {
			if res := h.runStream(t, thread7); res.Err != nil {
				t.Fatalf("stream: %v", res.Err)
			}
		},
	}
	for name, run := range paths {
		t.Run(name+" stdout decoy, real stderr header", func(t *testing.T) {
			h := newConvHarness(t, NewCodexProvider("img"))
			h.home.seed("codex", testSessB) // another conversation's thread
			h.store.PutSession(context.Background(), convChat, thread9, "codex", testSessB)
			h.home.codexStdoutDecoy = testSessB
			run(h, t)
			got := h.row(thread7)
			if got == testSessB || !validSessionID(got) || !h.home.has("codex", got) {
				t.Fatalf("thread row = %q; want the id from the stderr header, not the decoy %s", got, testSessB)
			}
		})
		t.Run(name+" stdout decoy only", func(t *testing.T) {
			h := newConvHarness(t, NewCodexProvider("img"))
			h.home.seed("codex", testSessB)
			h.home.codexQuiet = true
			h.home.codexStdoutDecoy = testSessB
			run(h, t)
			if got := h.row(thread7); got != "" {
				t.Fatalf("thread row = %q from a stdout-only decoy; want none", got)
			}
		})
		t.Run(name+" later stderr decoy", func(t *testing.T) {
			h := newConvHarness(t, NewCodexProvider("img"))
			h.home.seed("codex", testSessB)
			h.home.codexStderrDecoy = testSessB
			run(h, t)
			if got := h.row(thread7); got == testSessB || !validSessionID(got) {
				t.Fatalf("thread row = %q; the first stderr header must win", got)
			}
		})
	}
}

// stopHarness runs RunStream against fakeAgentExec, whose processes stay
// alive until the test ends them, so a Stop can overlap the next turn.
func newStopHarness(t *testing.T, prov AgentProvider) (*Runner, *fakeAgentExec, *fakeSessionStore) {
	t.Helper()
	fx := newFakeAgentExec()
	st := newFakeSessionStore()
	r := &Runner{
		ex:             fx,
		provider:       prov,
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		turnCount:      make(map[convKey]int),
		activity:       make(map[string]time.Time),
		memory:         nopMemory{},
		store:          st,
		containerLocks: make(map[string]*sync.Mutex),
		ensureFn: func(context.Context, string, Tier, *mountsec.Allowlist) (string, error) {
			return "cid", nil
		},
	}
	return r, fx, st
}

// A Stop during a new conversation's first turn: the queue starts the
// next turn while the killed agent is still shutting down. That turn
// must wait for it and then resume the session the first turn created,
// not start a second conversation (Codex) or drop the row because the
// transcript was not written yet (Claude).
func TestStopDuringFirstTurnNextTurnResumesIt(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			r, fx, st := newStopHarness(t, prov)
			fx.killGate = make(chan struct{})
			var transcript sync.Mutex
			written := false
			fx.onExec = func(cmd []string) *ExecResult {
				if len(cmd) > 0 && reflect.DeepEqual(cmd, prov.SessionExistsCmd(cmd[len(cmd)-1])) {
					transcript.Lock()
					defer transcript.Unlock()
					if written {
						return &ExecResult{}
					}
					return &ExecResult{ExitCode: 1}
				}
				return nil
			}

			ctx1, cancel1 := context.WithCancel(context.Background())
			defer cancel1()
			stream1, err := r.RunStream(ctx1, convChat, thread7, TierRegistered, nil, "first")
			if err != nil {
				t.Fatal(err)
			}
			p1 := <-fx.started
			first, resume := sessionOf(t, p1.cmd[4:])
			if resume {
				t.Fatalf("first turn resumed: %q", p1.cmd)
			}
			if prov.AnnouncesSessionID() {
				first = "c0dec0de-1111-4222-8333-444444444444"
				p1.stderrW.Write([]byte("session id: " + first + "\n"))
			}
			cancel1() // Stop; the kill hangs in its grace period
			go func() {
				for range stream1.Chunks {
				}
			}()

			second := make(chan *AgentStream, 1)
			go func() {
				s, err := r.RunStream(context.Background(), convChat, thread7, TierRegistered, nil, "second")
				if err != nil {
					t.Error(err)
				}
				second <- s
			}()
			select {
			case p := <-fx.started:
				t.Fatalf("next turn started while the stopped one was dying: %q", p.cmd)
			case <-time.After(200 * time.Millisecond):
			}

			// The dying agent saves its transcript, then exits.
			transcript.Lock()
			written = true
			transcript.Unlock()
			close(fx.killGate)

			select {
			case p2 := <-fx.started:
				wantResume(t, p2.cmd[4:], first)
				p2.exit(0)
			case <-time.After(5 * time.Second):
				t.Fatal("next turn never started")
			}
			if s := <-second; s != nil {
				readResult(t, s)
			}
			if got := st.row(convChat, thread7, prov.Name()); got != first {
				t.Fatalf("thread row = %q, want %q", got, first)
			}
		})
	}
}
