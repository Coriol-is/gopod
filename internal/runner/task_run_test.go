package runner

import (
	"context"
	"errors"
	"io"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// taskProc is one streamed exec of taskExec.
type taskProc struct {
	cmd, env []string
	outW     *io.PipeWriter
	errW     *io.PipeWriter
	done     chan error
	h        *StreamHandle
	once     sync.Once
}

// finish ends the process. observed says whether the daemon reported the exit.
func (p *taskProc) finish(code int, observed bool) {
	p.once.Do(func() {
		if observed {
			p.h.exit.code.Store(int64(code))
			p.h.exit.set.Store(true)
		}
		p.outW.Close()
		p.errW.Close()
		if code != 0 {
			p.done <- &ExecError{Code: code}
		}
		close(p.done)
	})
}

type taskExec struct {
	mu       sync.Mutex
	events   []string
	execs    [][]string
	procs    chan *taskProc
	all      []*taskProc
	killDies bool // the kill exec ends the process and the daemon reports it
}

func newTaskExec() *taskExec { return &taskExec{procs: make(chan *taskProc, 4), killDies: true} }

func (f *taskExec) event(s string) {
	f.mu.Lock()
	f.events = append(f.events, s)
	f.mu.Unlock()
}

func (f *taskExec) eventsSnapshot() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.events...)
}

func (f *taskExec) ExecStream(_ context.Context, _ string, cmd, env []string) (*StreamHandle, error) {
	f.event("exec_stream")
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &taskProc{cmd: cmd, env: env, outW: outW, errW: errW, done: make(chan error, 1)}
	p.h = &StreamHandle{Stdout: outR, Stderr: errR, Done: p.done, cancel: func() {
		// Like Docker: Close ends Done without any exit report.
		p.once.Do(func() { outW.Close(); errW.Close(); close(p.done) })
	}}
	f.mu.Lock()
	f.all = append(f.all, p)
	f.mu.Unlock()
	f.procs <- p
	return p.h, nil
}

func (f *taskExec) Exec(_ context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	f.mu.Unlock()
	if isKillCmd(cmd) {
		f.event("kill")
		if f.killDies {
			f.mu.Lock()
			procs := append([]*taskProc(nil), f.all...)
			f.mu.Unlock()
			for _, p := range procs {
				p.finish(143, true)
			}
		}
	}
	return &ExecResult{}, nil
}

func (f *taskExec) execsSnapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.execs...)
}

type taskHarness struct {
	*managedTestRunner
	fx      *taskExec
	exited  chan string
	spawned int
}

func newTaskHarness(t *testing.T, prov AgentProvider) *taskHarness {
	t.Helper()
	m := newManagedTestRunner(t, prov, allowShared)
	h := &taskHarness{managedTestRunner: m, fx: newTaskExec(), exited: make(chan string, 4)}
	m.r.ex = h.fx
	for _, p := range providersUnderTest() {
		m.r.managed.providers[p.Name()] = p
	}
	m.r.managed.exitWait = 50 * time.Millisecond
	return h
}

func (h *taskHarness) request(t *testing.T, folder, prompt string) TaskRunRequest {
	t.Helper()
	snap, err := h.r.ResolveTaskSnapshot(context.Background(), folder)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	return TaskRunRequest{
		Folder: folder, Prompt: prompt, SystemPrompt: "trusted run rules",
		Snapshot: TaskSnapshot(snap),
		Hooks: TaskHooks{
			BeginSpawn: func(context.Context) error { h.spawned++; h.fx.event("begin_spawn"); return nil },
			Exited:     func(ev string) { h.exited <- ev },
		},
	}
}

func drainStream(t *testing.T, s *AgentStream) (string, RunResult) {
	t.Helper()
	var sb strings.Builder
	go func() {}()
	for c := range s.Chunks {
		sb.WriteString(c)
	}
	select {
	case res := <-s.Result:
		return sb.String(), res
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	return "", RunResult{}
}

func nextProc(t *testing.T, fx *taskExec) *taskProc {
	t.Helper()
	select {
	case p := <-fx.procs:
		return p
	case <-time.After(5 * time.Second):
		t.Fatal("no exec started")
	}
	return nil
}

type recordingStore struct {
	mu    sync.Mutex
	calls []string
}

func (s *recordingStore) rec(c string) { s.mu.Lock(); s.calls = append(s.calls, c); s.mu.Unlock() }
func (s *recordingStore) GetState(context.Context, string) (string, error) {
	s.rec("GetState")
	return "", nil
}
func (s *recordingStore) SetState(context.Context, string, string) error {
	s.rec("SetState")
	return nil
}
func (s *recordingStore) GetSession(context.Context, string, int, string) (string, bool, error) {
	s.rec("GetSession")
	return "", false, nil
}
func (s *recordingStore) PutSession(context.Context, string, int, string, string) error {
	s.rec("PutSession")
	return nil
}
func (s *recordingStore) DeleteSession(context.Context, string, int, string) error {
	s.rec("DeleteSession")
	return nil
}

func TestRunTaskStreamBuildsEphemeralCommandForBothProviders(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			h := newTaskHarness(t, prov)
			st := &recordingStore{}
			h.r.store = st
			var extracted, compiled bool
			h.r.memory = memoryFunc(func() { compiled = true })
			h.r.extractFn = func(context.Context, string, int, string, string) { extracted = true }
			h.r.SetChatModel("owner", "model-x")
			req := h.request(t, "owner", "do the thing")
			stream, err := h.r.RunTaskStream(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			p := nextProc(t, h.fx)
			want := withModel(prov.(TaskCommandProvider).RunTaskCmd("do the thing", "trusted run rules"), prov, "model-x")
			if got := p.cmd[4:]; !reflect.DeepEqual(got, want) {
				t.Fatalf("exec = %q\nwant %q", got, want)
			}
			joined := strings.Join(want, " ")
			for _, banned := range []string{"--continue", "--resume", "--session-id", "resume", "--last"} {
				if slices.Contains(want, banned) {
					t.Errorf("task command contains %s: %s", banned, joined)
				}
			}
			switch prov.Name() {
			case "claude":
				if !slices.Contains(want, "--no-session-persistence") || !slices.Contains(want, "--append-system-prompt") {
					t.Errorf("claude task command: %s", joined)
				}
			case "codex":
				if !slices.Contains(want, "--ephemeral") || !strings.Contains(want[len(want)-1], "trusted run rules") {
					t.Errorf("codex task command: %s", joined)
				}
			}
			p.outW.Write([]byte("answer"))
			p.finish(0, true)
			out, res := drainStream(t, stream)
			if res.Err != nil || out != "answer" || res.FullText != "answer" {
				t.Fatalf("result = %q %+v", out, res)
			}
			if ev := <-h.exited; !strings.Contains(ev, "code=0") {
				t.Errorf("evidence = %q", ev)
			}
			if ev := h.fx.eventsSnapshot(); !slices.Equal(ev, []string{"begin_spawn", "exec_stream"}) {
				t.Errorf("order = %v", ev)
			}
			if len(st.calls) != 0 || extracted || compiled {
				t.Errorf("foreground/session machinery touched: store=%v extract=%v memory=%v", st.calls, extracted, compiled)
			}
			if n := h.r.getTurnCount("owner", 0); n != 0 {
				t.Errorf("turn count = %d", n)
			}
			for _, c := range h.fx.execsSnapshot() {
				for _, banned := range [][]string{prov.LatestSessionIDCmd(), prov.ClearSessionCmd(SessionRef{}), prov.AuthStatusCmd()} {
					if reflect.DeepEqual(c, banned) {
						t.Errorf("task path ran a session/auth command: %q", c)
					}
				}
			}
		})
	}
}

type memoryFunc func()

func (f memoryFunc) CompileContext(context.Context, string, int, string) string { f(); return "" }

func TestRunTaskStreamTimeoutKillsAndProvesExit(t *testing.T) {
	h := newTaskHarness(t, NewClaudeProvider("img"))
	req := h.request(t, "owner", "slow")
	req.Timeout = 80 * time.Millisecond
	stream, err := h.r.RunTaskStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	nextProc(t, h.fx)
	_, res := drainStream(t, stream)
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", res.Err)
	}
	if ev := <-h.exited; !strings.Contains(ev, "after_kill") {
		t.Errorf("evidence = %q", ev)
	}
	if !slices.Contains(h.fx.eventsSnapshot(), "kill") {
		t.Error("timeout did not kill the agent")
	}
}

// A kill that the daemon never confirms must not produce exit evidence.
func TestRunTaskStreamTimeoutWithoutExitReportIsNotProof(t *testing.T) {
	h := newTaskHarness(t, NewCodexProvider("img"))
	h.fx.killDies = false
	req := h.request(t, "owner", "stuck")
	req.Timeout = 50 * time.Millisecond
	stream, err := h.r.RunTaskStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	nextProc(t, h.fx)
	_, res := drainStream(t, stream)
	if !errors.Is(res.Err, context.DeadlineExceeded) {
		t.Fatalf("err = %v", res.Err)
	}
	select {
	case ev := <-h.exited:
		t.Fatalf("closing the attachment was reported as an exit: %q", ev)
	default:
	}
}

func TestRunTaskStreamParentCancelReportsCancelled(t *testing.T) {
	h := newTaskHarness(t, NewClaudeProvider("img"))
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := h.r.RunTaskStream(ctx, h.request(t, "owner", "x"))
	if err != nil {
		t.Fatal(err)
	}
	nextProc(t, h.fx)
	cancel()
	_, res := drainStream(t, stream)
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err = %v", res.Err)
	}
}

func TestRunTaskStreamNonzeroExitIsFailureWithEvidence(t *testing.T) {
	h := newTaskHarness(t, NewClaudeProvider("img"))
	stream, err := h.r.RunTaskStream(context.Background(), h.request(t, "owner", "x"))
	if err != nil {
		t.Fatal(err)
	}
	p := nextProc(t, h.fx)
	p.errW.Write([]byte("boom"))
	p.finish(3, true)
	_, res := drainStream(t, stream)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "exited 3") {
		t.Fatalf("err = %v", res.Err)
	}
	if ev := <-h.exited; !strings.Contains(ev, "code=3") {
		t.Errorf("evidence = %q", ev)
	}
}

func TestRunTaskStreamLifetimeBounds(t *testing.T) {
	base := TaskRunRequest{Folder: "f", Prompt: "p", Snapshot: TaskSnapshot{Provider: "claude", ProfileFingerprint: "fp"},
		Hooks: TaskHooks{BeginSpawn: func(context.Context) error { return nil }, Exited: func(string) {}}}
	if d, err := base.validate(); err != nil || d != 5*time.Minute {
		t.Fatalf("default = %v %v", d, err)
	}
	base.Timeout = MaxTaskTimeout
	if d, err := base.validate(); err != nil || d != 60*time.Minute {
		t.Fatalf("max = %v %v", d, err)
	}
	base.Timeout = MaxTaskTimeout + time.Second
	if _, err := base.validate(); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("over max: %v", err)
	}
	base.Timeout = -1
	if _, err := base.validate(); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("negative: %v", err)
	}
	base.Timeout = 0
	base.Hooks.Exited = nil
	if _, err := base.validate(); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("missing hook: %v", err)
	}
	big := TaskRunRequest{Folder: "f", Prompt: strings.Repeat("x", maxTaskArgBytes+1)}
	if _, err := big.validate(); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("large prompt: %v", err)
	}
}

// Every failure before the marker must leave the ledger untouched and the slot free.
func TestRunTaskStreamPreSpawnDenials(t *testing.T) {
	t.Run("changed policy", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		h.scopes.bump("owner")
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrTaskDenied) {
			t.Fatalf("err = %v", err)
		}
		if h.spawned != 0 {
			t.Fatal("marker committed for a denied attempt")
		}
	})
	t.Run("wrong container", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		// A container with this name exists but carries no policy labels.
		if _, err := h.ensureAs(RoleForegroundPersonal); err != nil {
			t.Fatal(err)
		}
		h.eng.get("gopod-owner").Labels = map[string]string{}
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrPolicyUnknown) {
			t.Fatalf("err = %v", err)
		}
		if h.spawned != 0 || len(h.fx.eventsSnapshot()) != 0 {
			t.Fatal("ran in an untrusted container")
		}
	})
	t.Run("unqualified", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		h.r.managed.qualify = nil
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrTaskRoleUnqualified) {
			t.Fatalf("err = %v", err)
		}
		if h.spawned != 0 {
			t.Fatal("marker committed")
		}
	})
	t.Run("not admitted folder", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		req.Folder = "stranger"
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrTaskDenied) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("provider without task command", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		h.r.managed.providers["claude"] = bareProvider{NewClaudeProvider("img")}
		h.r.provider = bareProvider{NewClaudeProvider("img")}
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrTaskDenied) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("unknown snapshot provider", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		req.Snapshot.Provider = "other"
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, ErrTaskDenied) {
			t.Fatalf("err = %v", err)
		}
	})
	t.Run("marker refused", func(t *testing.T) {
		h := newTaskHarness(t, NewClaudeProvider("img"))
		req := h.request(t, "owner", "x")
		refused := errors.New("refused")
		req.Hooks.BeginSpawn = func(context.Context) error { return refused }
		if _, err := h.r.RunTaskStream(context.Background(), req); !errors.Is(err, refused) {
			t.Fatalf("err = %v", err)
		}
		if len(h.fx.eventsSnapshot()) != 0 {
			t.Fatal("native exec requested after a refused marker")
		}
		// the slot is free again
		rel, err := h.r.acquireAgentSlot(context.Background(), "owner")
		if err != nil {
			t.Fatal(err)
		}
		rel()
	})
}

// bareProvider hides RunTaskCmd.
type bareProvider struct{ AgentProvider }

func (h *managedTestRunner) ensureAs(role ExecRole) (string, error) {
	rel, err := h.r.acquireAgentSlot(context.Background(), "owner")
	if err != nil {
		return "", err
	}
	defer rel()
	return h.r.ensureRole(context.Background(), "owner", role, h.r.provider, nil, false, "")
}

func TestProviderSwitchWhileQueuedVersusStarted(t *testing.T) {
	h := newTaskHarness(t, NewClaudeProvider("img-claude"))
	codex := NewCodexProvider("img-codex")
	h.r.managed.providers["codex"] = codex
	h.r.managed.providers["claude"] = h.r.provider

	// Switched while queued (before the snapshot): the snapshot sees codex.
	h.r.setChatProviderOverride("owner", codex)
	queued := h.request(t, "owner", "q")
	if queued.Snapshot.Provider != "codex" {
		t.Fatalf("queued snapshot = %+v", queued.Snapshot)
	}

	// Snapshot first, then the chat switches back: the started attempt keeps codex.
	started := h.request(t, "owner", "s")
	h.r.setChatProviderOverride("owner", h.r.provider)
	stream, err := h.r.RunTaskStream(context.Background(), started)
	if err != nil {
		t.Fatal(err)
	}
	p := nextProc(t, h.fx)
	if p.cmd[4] != "codex" {
		t.Fatalf("started attempt ran %q, want its snapshot provider codex", p.cmd[4])
	}
	if c := h.eng.get("gopod-owner"); c == nil || !strings.HasSuffix(c.ImageID, "img-codex") {
		t.Fatalf("container image = %+v", c)
	}
	p.finish(0, true)
	drainStream(t, stream)
}

func TestRunTaskStreamInjectsOnlyHostEnvNoMemoryCapability(t *testing.T) {
	h := newTaskHarness(t, NewClaudeProvider("img"))
	h.r.SetCapabilities(&neverMinter{t: t})
	h.r.nodeHeapMB = 512
	req := h.request(t, "owner", "x")
	req.Env = []string{"GOPOD_TASK_RUN=r"}
	stream, err := h.r.RunTaskStream(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	p := nextProc(t, h.fx)
	if !slices.Contains(p.env, "GOPOD_TASK_RUN=r") {
		t.Errorf("env = %v", p.env)
	}
	var heap bool
	for _, kv := range p.env {
		if strings.HasPrefix(kv, "NODE_OPTIONS=") && strings.Contains(kv, "--max-old-space-size=512") {
			heap = true
		}
	}
	if !heap {
		t.Errorf("claude task lost its heap cap: %v", p.env)
	}
	p.finish(0, true)
	drainStream(t, stream)
}

type neverMinter struct{ t *testing.T }

func (m *neverMinter) Mint(string, int) string {
	m.t.Error("a task run minted a conversation memory capability")
	return "x"
}
func (*neverMinter) Revoke(string) {}
