package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func TestWrapWithPidFileArgv(t *testing.T) {
	got := wrapWithPidFile("/tmp/gopod-agent-x.pid", []string{"claude", "-p", `it's "quoted"`})
	want := []string{
		"sh", "-c", `echo $$ > "$0"; exec "$@"`, "/tmp/gopod-agent-x.pid",
		"claude", "-p", `it's "quoted"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("argv =\n%q\nwant\n%q", got, want)
	}
}

func TestNewAgentPidFileUniqueUnderTmp(t *testing.T) {
	a, err := newAgentPidFile()
	if err != nil {
		t.Fatal(err)
	}
	b, _ := newAgentPidFile()
	if a == b {
		t.Fatalf("pid files not unique: %q", a)
	}
	if !strings.HasPrefix(a, "/tmp/gopod-agent-") || !strings.HasSuffix(a, ".pid") {
		t.Fatalf("pid file = %q", a)
	}
}

func TestKillAgentCmdShape(t *testing.T) {
	cmd := killAgentCmd("/tmp/p.pid", 3*time.Second)
	if len(cmd) != 4 || cmd[0] != "sh" || cmd[1] != "-c" || cmd[3] != "/tmp/p.pid" {
		t.Fatalf("cmd = %q", cmd)
	}
	for _, want := range []string{`kill -TERM "-$p"`, `kill -KILL "-$p"`, `[ $i -lt 30 ]`, `rm -f "$f"`, "exit 0"} {
		if !strings.Contains(cmd[2], want) {
			t.Errorf("script lacks %q:\n%s", want, cmd[2])
		}
	}
}

// TestKillAgentScriptsLocally runs the wrapper and the kill script with
// the host's sh: the wrapped command (a shell with a child sleep, like
// an agent running a tool) must be gone, child included, and the pid
// file removed.
func TestKillAgentScriptsLocally(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	pidFile := filepath.Join(t.TempDir(), "agent.pid")
	childPidFile := filepath.Join(t.TempDir(), "child.pid")
	argv := wrapWithPidFile(pidFile, []string{"sh", "-c", `sleep 300 & echo $! > "$1"; wait`, "sh", childPidFile})
	agent := exec.Command(argv[0], argv[1:]...)
	// docker exec makes the process a group leader (runc setsid).
	agent.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := agent.Start(); err != nil {
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() { waited <- agent.Wait() }()

	childPid := waitPidFile(t, childPidFile)
	if got := waitPidFile(t, pidFile); got != agent.Process.Pid {
		t.Fatalf("pid file = %d, want the agent's pid %d (exec keeps it)", got, agent.Process.Pid)
	}

	kill := killAgentCmd(pidFile, time.Second)
	if out, err := exec.Command(kill[0], kill[1:]...).CombinedOutput(); err != nil {
		t.Fatalf("kill script: %v: %s", err, out)
	}
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("agent still running after kill script")
	}
	if _, err := os.Stat(pidFile); !os.IsNotExist(err) {
		t.Errorf("pid file still there: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for syscall.Kill(childPid, 0) == nil {
		if time.Now().After(deadline) {
			_ = syscall.Kill(childPid, syscall.SIGKILL)
			t.Fatal("agent's child survived the group kill")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func waitPidFile(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b, err := os.ReadFile(path)
		if err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(b))); err == nil {
				return pid
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("pid file %s never written", path)
	return 0
}

// --- startStream with a fake Docker -----------------------------------

// fakeProc is one streamed exec of fakeAgentExec.
type fakeProc struct {
	cmd     []string
	stdoutW *io.PipeWriter
	stderrW *io.PipeWriter
	done    chan error
	once    sync.Once
}

// exit ends the process: output EOF, then the exit status on Done.
func (p *fakeProc) exit(code int) {
	p.once.Do(func() {
		p.stdoutW.Close()
		p.stderrW.Close()
		if code != 0 {
			p.done <- &ExecError{Code: code}
		}
		close(p.done)
	})
}

type fakeAgentExec struct {
	mu      sync.Mutex
	execs   [][]string
	procs   []*fakeProc
	started chan *fakeProc
	// killDies makes the kill exec end the process (143, like SIGTERM).
	killDies bool
	// killGate, if set, blocks the kill exec until closed (a slow grace).
	killGate chan struct{}
}

func newFakeAgentExec() *fakeAgentExec {
	return &fakeAgentExec{started: make(chan *fakeProc, 4), killDies: true}
}

func (f *fakeAgentExec) ExecStream(_ context.Context, _ string, cmd, _ []string) (*StreamHandle, error) {
	outR, outW := io.Pipe()
	errR, errW := io.Pipe()
	p := &fakeProc{cmd: cmd, stdoutW: outW, stderrW: errW, done: make(chan error, 1)}
	f.mu.Lock()
	f.procs = append(f.procs, p)
	f.mu.Unlock()
	f.started <- p
	// Like Docker: Close cancels the exec context, which ends Done.
	cancel := func() { p.once.Do(func() { outW.Close(); errW.Close(); close(p.done) }) }
	return &StreamHandle{Stdout: outR, Stderr: errR, Done: p.done, cancel: cancel}, nil
}

func (f *fakeAgentExec) Exec(_ context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	f.mu.Lock()
	f.execs = append(f.execs, cmd)
	procs := append([]*fakeProc(nil), f.procs...)
	f.mu.Unlock()
	if isKillCmd(cmd) {
		if f.killGate != nil {
			<-f.killGate
		}
		if f.killDies {
			for _, p := range procs {
				if p.cmd[3] == cmd[3] {
					p.exit(143)
				}
			}
		}
	}
	return &ExecResult{}, nil
}

func (f *fakeAgentExec) execsSnapshot() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([][]string(nil), f.execs...)
}

func isKillCmd(cmd []string) bool {
	return len(cmd) == 4 && cmd[0] == "sh" && strings.Contains(cmd[2], "kill -TERM")
}

func isRmCmd(cmd []string) bool { return len(cmd) == 3 && cmd[0] == "rm" && cmd[1] == "-f" }

type nopMemory struct{}

func (nopMemory) CompileContext(context.Context, string, int, string) string { return "" }

type streamTestRunner struct {
	r        *Runner
	fx       *fakeAgentExec
	extracts chan string
}

func newStreamTestRunner(t *testing.T) *streamTestRunner {
	t.Helper()
	fx := newFakeAgentExec()
	extracts := make(chan string, 4)
	r := &Runner{
		ex:           fx,
		log:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		turnCount:    make(map[string]int),
		activity:     make(map[string]time.Time),
		compactAfter: 30,
		memory:       nopMemory{},
	}
	r.SetExtractFn(func(_ context.Context, _, _, reply string) { extracts <- reply })
	return &streamTestRunner{r: r, fx: fx, extracts: extracts}
}

func (s *streamTestRunner) start(t *testing.T, ctx context.Context, prov AgentProvider) (*AgentStream, *fakeProc) {
	t.Helper()
	cmd := prov.RunCmd("hi", "", SessionRef{})
	stream, err := s.r.startStream(ctx, "cid", "chat", TierRegistered, nil, prov, "hi", cmd)
	if err != nil {
		t.Fatalf("startStream: %v", err)
	}
	p := <-s.fx.started
	if !reflect.DeepEqual(p.cmd, wrapWithPidFile(p.cmd[3], cmd)) {
		t.Fatalf("exec cmd not the wrapped provider cmd: %q", p.cmd)
	}
	return stream, p
}

func readResult(t *testing.T, stream *AgentStream) RunResult {
	t.Helper()
	go func() {
		for range stream.Chunks {
		}
	}()
	select {
	case res := <-stream.Result:
		return res
	case <-time.After(5 * time.Second):
		t.Fatal("no result")
	}
	return RunResult{}
}

func providersUnderTest() []AgentProvider {
	return []AgentProvider{NewClaudeProvider("img"), NewCodexProvider("img")}
}

func TestStartStreamNormalRunIngestsAndRemovesPidFile(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			s := newStreamTestRunner(t)
			stream, p := s.start(t, context.Background(), prov)
			go func() {
				p.stdoutW.Write([]byte("hello"))
				p.exit(0)
			}()
			res := readResult(t, stream)
			if res.Err != nil || res.FullText != "hello" {
				t.Fatalf("result = %+v", res)
			}
			select {
			case got := <-s.extracts:
				if got != "hello" {
					t.Errorf("extracted %q", got)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("reply not ingested")
			}
			if n := s.r.getTurnCount("chat"); n != 1 {
				t.Errorf("turn count = %d, want 1", n)
			}
			var rm, kill int
			for _, c := range s.fx.execsSnapshot() {
				if isRmCmd(c) && c[2] == p.cmd[3] {
					rm++
				}
				if isKillCmd(c) {
					kill++
				}
			}
			if rm != 1 || kill != 0 {
				t.Errorf("rm execs = %d, kill execs = %d; want 1, 0", rm, kill)
			}
		})
	}
}

func TestStartStreamFailedRunKeepsTodaysBehaviour(t *testing.T) {
	s := newStreamTestRunner(t)
	stream, p := s.start(t, context.Background(), NewClaudeProvider("img"))
	go func() {
		p.stderrW.Write([]byte("boom"))
		p.exit(2)
	}()
	res := readResult(t, stream)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "agent exited 2") {
		t.Fatalf("err = %v", res.Err)
	}
	if errors.Is(res.Err, context.Canceled) {
		t.Fatal("failed run reported as cancelled")
	}
}

func TestStartStreamCancelKillsAndSkipsPostRun(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			s := newStreamTestRunner(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			stream, p := s.start(t, ctx, prov)

			go p.stdoutW.Write([]byte("partial"))
			if c := <-stream.Chunks; c != "partial" {
				t.Fatalf("chunk = %q", c)
			}
			cancel()

			res := readResult(t, stream)
			if !errors.Is(res.Err, context.Canceled) {
				t.Fatalf("err = %v, want context.Canceled", res.Err)
			}
			var kills, rms int
			for _, c := range s.fx.execsSnapshot() {
				if isKillCmd(c) {
					kills++
					if c[3] != p.cmd[3] {
						t.Errorf("kill targets %q, run's pid file is %q", c[3], p.cmd[3])
					}
				}
				if isRmCmd(c) {
					rms++
				}
			}
			if kills != 1 {
				t.Errorf("kill execs = %d, want 1", kills)
			}
			if rms != 0 {
				t.Errorf("rm execs = %d, want 0 (the kill script removes the pid file)", rms)
			}
			select {
			case got := <-s.extracts:
				t.Fatalf("cancelled turn ingested: %q", got)
			case <-time.After(200 * time.Millisecond):
			}
			if n := s.r.getTurnCount("chat"); n != 0 {
				t.Errorf("turn count = %d, want 0", n)
			}
		})
	}
}

// A kill that fails (the process keeps running) must still end the
// stream: the handle is closed so the read loop and Result finish.
func TestStartStreamCancelEndsEvenIfKillFails(t *testing.T) {
	s := newStreamTestRunner(t)
	s.fx.killDies = false
	ctx, cancel := context.WithCancel(context.Background())
	stream, _ := s.start(t, ctx, NewClaudeProvider("img"))
	cancel()
	res := readResult(t, stream)
	if !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("err = %v", res.Err)
	}
}

// The consumer may stop reading Chunks once it cancels; the runner must
// not block on them.
func TestStartStreamCancelWithAbandonedChunks(t *testing.T) {
	s := newStreamTestRunner(t)
	ctx, cancel := context.WithCancel(context.Background())
	stream, p := s.start(t, ctx, NewClaudeProvider("img"))
	s.fx.killGate = make(chan struct{})
	cancel()
	// Flood output while the kill is in its grace period; nobody reads.
	go func() {
		for i := 0; i < 100; i++ {
			if _, err := p.stdoutW.Write([]byte("x")); err != nil {
				return
			}
		}
	}()
	time.Sleep(50 * time.Millisecond)
	close(s.fx.killGate)
	select {
	case res := <-stream.Result:
		if !errors.Is(res.Err, context.Canceled) {
			t.Fatalf("err = %v", res.Err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("runner goroutine blocked on unread chunks")
	}
}

// Next-turn safety: a stopped turn's agent holds the chat's slot until
// the kill is over, so the next turn's exec does not start before it.
func TestStartStreamNextTurnWaitsForKill(t *testing.T) {
	s := newStreamTestRunner(t)
	s.fx.killGate = make(chan struct{})
	prov := NewClaudeProvider("img")

	ctx1, cancel1 := context.WithCancel(context.Background())
	stream1, _ := s.start(t, ctx1, prov)
	cancel1()
	go func() {
		for range stream1.Chunks {
		}
	}()

	second := make(chan error, 1)
	go func() {
		_, err := s.r.startStream(context.Background(), "cid", "chat", TierRegistered, nil, prov, "next", prov.RunCmd("next", "", SessionRef{}))
		second <- err
	}()

	select {
	case p := <-s.fx.started:
		t.Fatalf("second turn started while the first was being killed: %q", p.cmd)
	case <-time.After(200 * time.Millisecond):
	}

	close(s.fx.killGate) // grace over, first agent dies
	select {
	case p := <-s.fx.started:
		if p.cmd[len(p.cmd)-1] != "next" {
			t.Fatalf("unexpected exec %q", p.cmd)
		}
		p.exit(0)
	case <-time.After(5 * time.Second):
		t.Fatal("second turn never started")
	}
	if err := <-second; err != nil {
		t.Fatal(err)
	}
	if res := <-stream1.Result; !errors.Is(res.Err, context.Canceled) {
		t.Fatalf("first result = %+v", res)
	}
}

func TestAcquireAgentSlotRespectsCtx(t *testing.T) {
	r := &Runner{}
	release, err := r.acquireAgentSlot(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := r.acquireAgentSlot(ctx, "chat"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if rel, err := r.acquireAgentSlot(context.Background(), "other"); err != nil {
		t.Fatalf("other chat blocked: %v", err)
	} else {
		rel()
	}
	release()
	rel, err := r.acquireAgentSlot(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	rel()
}
