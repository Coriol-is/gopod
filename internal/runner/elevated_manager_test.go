package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

type elevatedFake struct {
	*fakeEngine
	run       func(context.Context) (*ExecResult, error)
	removeErr error
}

func (f *elevatedFake) ElevatedIDs(context.Context) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for _, c := range f.containers {
		if c.Labels[LabelPolicyRole] == string(RoleElevatedHost) {
			ids = append(ids, c.ID)
		}
	}
	return ids, f.inspectErr
}
func (f *elevatedFake) ExecElevated(ctx context.Context, _ string, _ []string) (*ExecResult, error) {
	if f.run != nil {
		return f.run(ctx)
	}
	return &ExecResult{Stdout: "ok"}, nil
}
func (f *elevatedFake) StopRemove(ctx context.Context, id string) error {
	if f.removeErr != nil {
		return f.removeErr
	}
	return f.fakeEngine.StopRemove(ctx, id)
}
func elevatedFixture(t *testing.T) (*Elevated, *elevatedFake) {
	t.Helper()
	root := t.TempDir()
	for _, p := range []string{"data", "chats"} {
		if err := os.Mkdir(filepath.Join(root, p), 0700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(t.Context(), filepath.Join(root, "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := NewElevated(st, nil, Paths{RepoRoot: root, DataDir: filepath.Join(root, "data"), ChatsDir: filepath.Join(root, "chats"), ContainerSkillsDir: root, EmptyFile: filepath.Join(root, "empty")}, goodElevatedConfig(), "codex-elevated:test")
	f := &elevatedFake{fakeEngine: newFakeEngine()}
	e.engine = f
	return e, f
}
func TestElevatedProviderReopenAndLostRuntime(t *testing.T) {
	e, f := elevatedFixture(t)
	ctx := t.Context()
	if _, err := e.Start(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	if err := e.Close(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	if e.base.Image != goodElevatedConfig().Image {
		t.Fatal("inherited codex image")
	}
	f.mu.Lock()
	delete(f.containers, ContainerName(ElevatedFolder))
	n := f.nextID
	f.mu.Unlock()
	if _, err := e.Run(ctx, "hello"); err == nil {
		t.Fatal("lost runtime accepted")
	}
	if f.nextID != n {
		t.Fatal("lost runtime recreated")
	}
}
func TestElevatedCloseFencesConcurrentRunAndRenamedSurvivor(t *testing.T) {
	e, f := elevatedFixture(t)
	ctx := t.Context()
	if _, err := e.Start(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	release := make(chan struct{})
	f.run = func(ctx context.Context) (*ExecResult, error) {
		close(entered)
		<-ctx.Done()
		<-release
		return &ExecResult{ExitCode: 137}, nil
	}
	runDone := make(chan struct{})
	go func() { defer close(runDone); _, _ = e.Run(ctx, "wait") }()
	<-entered
	closeDone := make(chan error, 1)
	go func() { closeDone <- e.Close(ctx, "owner") }()
	// cancellation proves Close crossed its admission fence.
	e.mu.Lock()
	cancel := e.cancel
	e.mu.Unlock()
	_ = cancel
	for {
		e.mu.Lock()
		blocked := e.blocked
		e.mu.Unlock()
		if blocked {
			break
		}
		runtime.Gosched()
	}
	if _, err := e.Run(ctx, "race"); err == nil {
		t.Fatal("accepted during close")
	}
	close(release)
	<-runDone
	if err := <-closeDone; err != nil {
		t.Fatal(err)
	}
	if _, err := e.Start(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	f.containers["renamed"] = f.containers[ContainerName(ElevatedFolder)]
	delete(f.containers, ContainerName(ElevatedFolder))
	f.mu.Unlock()
	if err := e.Close(ctx, "renamed"); err != nil {
		t.Fatal(err)
	}
	if len(f.containers) != 0 || e.Active() {
		t.Fatal("renamed survivor accepted as absent")
	}
}
func TestElevatedFailureKeepsModalAndRecoverCloses(t *testing.T) {
	e, f := elevatedFixture(t)
	ctx := t.Context()
	if _, err := e.Start(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	f.removeErr = errors.New("Docker down")
	if e.Close(ctx, "test") == nil || !e.Active() {
		t.Fatal("false closure")
	}
	if _, err := e.Run(ctx, "no"); err == nil {
		t.Fatal("ran after unknown close")
	}
	f.removeErr = nil
	if err := e.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if e.Active() {
		t.Fatal("still active after recovery")
	}
	if !strings.Contains(e.TakeNotice(), "прервана") {
		t.Fatal("no recovery notice")
	}
}
func TestElevatedCommandsUseExplicitAuthorityAndNativeIdentity(t *testing.T) {
	ref := SessionRef{ID: "12345678-1234-1234-1234-123456789abc"}
	cmd := strings.Join(elevatedCommand(NewClaudeProvider(""), "hello", ref), " ")
	if strings.Contains(cmd, "dangerously-skip") || !strings.Contains(cmd, "dontAsk") || !strings.Contains(cmd, "--session-id "+ref.ID) {
		t.Fatal(cmd)
	}
	c := strings.Join(elevatedCommand(NewCodexProvider(""), "hello", ref), " ")
	if strings.Contains(c, "--last") || !strings.Contains(c, "--json") {
		t.Fatal(c)
	}
}

func TestElevatedRecoveryWithoutDockerRetainsModalFence(t *testing.T) {
	e, _ := elevatedFixture(t)
	if _, err := e.st.OpenElevatedSession(t.Context(), "claude"); err != nil {
		t.Fatal(err)
	}
	e.engine = nil
	if err := e.Recover(t.Context()); err == nil || !e.Active() {
		t.Fatal("forgot durable live generation", err)
	}
	if _, err := e.Start(t.Context(), "claude"); err == nil {
		t.Fatal("reopened without death proof")
	}
}
func TestElevatedRecoveryInventoryFailureBlocksStart(t *testing.T) {
	e, f := elevatedFixture(t)
	f.inspectErr = errors.New("inventory offline")
	if e.Recover(t.Context()) == nil || !e.Active() {
		t.Fatal("inventory failure opened admission")
	}
}
func TestElevatedCancelledFirstCodexRetainsNativeContext(t *testing.T) {
	e, f := elevatedFixture(t)
	if _, err := e.Start(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	const native = "12345678-1234-1234-1234-123456789abc"
	entered := make(chan struct{})
	f.run = func(ctx context.Context) (*ExecResult, error) {
		close(entered)
		<-ctx.Done()
		return &ExecResult{Stdout: `{"type":"thread.started","thread_id":"` + native + `"}`, ExitCode: 137}, nil
	}
	done := make(chan error, 1)
	go func() { _, err := e.Run(t.Context(), "hello"); done <- err }()
	<-entered
	e.StopAnswer()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	id, err := e.st.ElevatedNativeContext(t.Context(), e.session.ID)
	if err != nil || id != native {
		t.Fatal(id, err)
	}
}

func TestElevatedHoldsGlobalCapacityUntilUnknownRuntimeClosed(t *testing.T) {
	e, f := elevatedFixture(t)
	e.admission = &Runner{gate: newGate(1, 0, nil, quietLog()), log: quietLog()}
	if _, err := e.Start(t.Context(), "codex"); err != nil {
		t.Fatal(err)
	}
	f.run = func(context.Context) (*ExecResult, error) { return nil, ErrExecutionUnproven }
	if _, err := e.Run(t.Context(), "uncertain"); err == nil {
		t.Fatal("unknown run succeeded")
	}
	running, _ := e.admission.gate.counts()
	if running != 1 {
		t.Fatal("released unknown capacity", running)
	}
	if err := e.Close(t.Context(), "owner"); err != nil {
		t.Fatal(err)
	}
	running, _ = e.admission.gate.counts()
	if running != 0 {
		t.Fatal("leaked capacity after proof", running)
	}
}

func (f *elevatedFake) ElevatedSupported(context.Context) error { return nil }
