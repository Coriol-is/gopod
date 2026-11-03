package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

const restartHost = "123456abcdef"

type restartFakeDocker struct {
	self    container.InspectResponse
	targets map[string]container.InspectResponse
	list    []container.Summary
	stopped []string
}

func (d *restartFakeDocker) ContainerInspect(_ context.Context, id string) (container.InspectResponse, error) {
	if id == restartHost {
		return d.self, nil
	}
	if c, ok := d.targets[id]; ok {
		return c, nil
	}
	return container.InspectResponse{}, errors.New("missing")
}
func (d *restartFakeDocker) ContainerStop(_ context.Context, id string, _ container.StopOptions) error {
	d.stopped = append(d.stopped, id)
	c := d.targets[id]
	c.State.Running = false
	d.targets[id] = c
	return nil
}
func (d *restartFakeDocker) ContainerList(context.Context, container.ListOptions) ([]container.Summary, error) {
	return d.list, nil
}

func supervisedFixture() container.InspectResponse {
	return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: restartHost + strings.Repeat("a", 52), State: &container.State{Running: true, StartedAt: time.Now().Add(-time.Hour).Format(time.RFC3339Nano)}, HostConfig: &container.HostConfig{RestartPolicy: container.RestartPolicy{Name: "unless-stopped"}}}, Config: &container.Config{Hostname: restartHost, Labels: map[string]string{"com.docker.compose.service": "gopod"}}}
}

func TestRestartSupervisorEvidence(t *testing.T) {
	c := supervisedFixture()
	if !restartSupervisor(c, restartHost, time.Now()) {
		t.Fatal("valid supervised controller refused")
	}
	c.HostConfig.RestartPolicy.Name = "no"
	if restartSupervisor(c, restartHost, time.Now()) {
		t.Fatal("unmanaged accepted")
	}
	c = supervisedFixture()
	c.State.StartedAt = time.Now().Format(time.RFC3339Nano)
	if restartSupervisor(c, restartHost, time.Now()) {
		t.Fatal("startup grace ignored")
	}
	c = supervisedFixture()
	c.Config.Labels[runner.LabelChat] = "owner"
	if restartSupervisor(c, restartHost, time.Now()) {
		t.Fatal("agent confused with controller")
	}
}

func restartFixture(t *testing.T) (*controllerRestart, *restartFakeDocker, *int) {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	d := &restartFakeDocker{self: supervisedFixture(), targets: map[string]container.InspectResponse{}}
	d.targets["old"] = container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{ID: "old", State: &container.State{Running: true}}, Config: &container.Config{Labels: map[string]string{runner.LabelChat: "owner"}}, Mounts: []container.MountPoint{{Type: mount.TypeBind, Source: filepath.Join(root, "owner"), Destination: "/workspace/chat"}}}
	d.list = []container.Summary{{ID: "old", State: "running", Labels: map[string]string{runner.LabelChat: "owner"}}}
	stops := new(int)
	r := newControllerRestart(st, root, 1, func() { *stops++ }, slog.New(slog.NewTextHandler(io.Discard, nil)))
	r.hostname = restartHost
	r.docker = d
	r.queue = queue.New(t.Context(), func(context.Context, []queue.Item) error { return nil }, 1, nil, nil)
	r.runtime = &instantDrain{}
	t.Cleanup(r.queue.Close)
	return r, d, stops
}

func TestRestartRequestDurableBeforeNoticeAndShutdown(t *testing.T) {
	r, d, stops := restartFixture(t)
	r.notify = func(ctx context.Context, chat int64, thread int, text string) error {
		record, err := r.st.ReadRestart(ctx)
		if err != nil || record.Phase != "requested" || len(record.Containers) != 1 {
			t.Fatal(record, err)
		}
		if chat != 1 || thread != 9 || *stops != 0 || len(d.stopped) != 0 {
			t.Fatal("wrong ordering or destination")
		}
		return nil
	}
	caller := control.Caller{ChatID: 1, ThreadID: 9, MessageID: 42}
	if err := r.request(t.Context(), caller); err != nil {
		t.Fatal(err)
	}
	waitRestart(t, r)
	if *stops != 1 || !r.requested.Load() {
		t.Fatal("no graceful shutdown")
	}
	if err := r.request(t.Context(), caller); err == nil || *stops != 1 {
		t.Fatal("restart replay")
	}
	if err := r.recycle(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(d.stopped) != 1 || d.stopped[0] != "old" {
		t.Fatal(d.stopped)
	}
	if err := r.recycle(t.Context()); err != nil || len(d.stopped) != 1 {
		t.Fatal("non-idempotent recycle", err)
	}
}

func TestRestartNoticeUnknownDoesNotStop(t *testing.T) {
	r, _, stops := restartFixture(t)
	r.notify = func(context.Context, int64, int, string) error { return context.DeadlineExceeded }
	if err := r.request(t.Context(), control.Caller{ChatID: 1, MessageID: 1}); err == nil || *stops != 0 {
		t.Fatal(err, *stops)
	}
	record, _ := r.st.ReadRestart(t.Context())
	if record.Phase != "notice_unknown" {
		t.Fatal(record)
	}
}

func TestRestartRecoveryNoticeIsOneShotAndTruthful(t *testing.T) {
	r, _, stops := restartFixture(t)
	if err := r.st.BeginRestart(t.Context(), store.RestartRecord{ChatID: 1, ThreadID: 8, MessageID: 2}); err != nil {
		t.Fatal(err)
	}
	if err := r.st.SetRestartPhase(t.Context(), 2, "requested", "restarting"); err != nil {
		t.Fatal(err)
	}
	calls := 0
	r.notify = func(ctx context.Context, chat int64, thread int, text string) error {
		calls++
		record, _ := r.st.ReadRestart(ctx)
		if record.Phase != "notice_claimed" || thread != 8 || !strings.Contains(text, "не подтверждены") {
			t.Fatal(record, text)
		}
		return context.DeadlineExceeded
	}
	if !r.recovered(t.Context(), nil, nil) {
		t.Fatal("not recovered")
	}
	if r.recovered(t.Context(), nil, nil) || calls != 1 || *stops != 0 {
		t.Fatal("replayed recovery or restarted controller")
	}
	record, _ := r.st.ReadRestart(t.Context())
	if record.Phase != "notice_unknown" {
		t.Fatal(record)
	}
}

func TestRestartCannotStopOtherWorkspace(t *testing.T) {
	r, d, _ := restartFixture(t)
	sc, err := r.st.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	c := d.targets["old"]
	c.Mounts[0].Source = "/other/project/owner"
	d.targets["old"] = c
	if err = r.stopTarget(t.Context(), store.RestartContainer{ID: "old", Scope: sc}); err == nil || len(d.stopped) > 0 {
		t.Fatal("cross-instance stop", err)
	}
}

func TestRestartReadinessMatchesSelectedRuntime(t *testing.T) {
	r, d, _ := restartFixture(t)
	info := d.targets["old"]
	info.Config.Image = "selected:tag"
	report := runner.StartupReport{Image: "selected:tag"}
	workspace := filepath.Join(r.chatsDir, "owner")
	if !restartRuntimeReady(info, report, "owner", workspace) {
		t.Fatal("matching runtime refused")
	}
	info.Config.Labels[runner.LabelPolicyRole] = string(runner.RoleScheduledTaskPersonal)
	if restartRuntimeReady(info, report, "owner", workspace) {
		t.Fatal("task role reported ready for foreground")
	}
	info.Config.Labels[runner.LabelPolicyRole] = string(runner.RoleForegroundPersonal)
	info.Config.Image = "other:tag"
	if restartRuntimeReady(info, report, "owner", workspace) {
		t.Fatal("wrong provider image reported ready")
	}
}

func TestRestartFailedPhaseWriteRepairsUnstartedReceipt(t *testing.T) {
	r, _, stops := restartFixture(t)
	if _, err := r.st.DB().Exec(`CREATE TRIGGER refuse_restart_phase BEFORE UPDATE ON router_state WHEN NEW.key='controller:restart' AND NEW.value LIKE '%"phase":"draining"%' BEGIN SELECT RAISE(ABORT,'phase failure'); END`); err != nil {
		t.Fatal(err)
	}
	r.notify = func(context.Context, int64, int, string) error { return nil }
	if err := r.request(t.Context(), control.Caller{ChatID: 1, MessageID: 12}); err == nil || *stops != 0 {
		t.Fatal(err, *stops)
	}
	record, err := r.st.ReadRestart(t.Context())
	if err != nil || record.Phase != "aborted" {
		t.Fatal(record, err)
	}
}

type instantDrain struct{}

func (*instantDrain) BeginDrain()                       {}
func (*instantDrain) CancelDrain()                      {}
func (*instantDrain) CancelActive()                     {}
func (*instantDrain) WaitDrained(context.Context) error { return nil }
func (*instantDrain) ForceDrain(context.Context) error  { return nil }
func waitRestart(t *testing.T, r *controllerRestart) {
	t.Helper()
	deadline := time.After(2 * time.Second)
	for {
		r.mu.Lock()
		done := r.requested.Load()
		r.mu.Unlock()
		if done {
			return
		}
		select {
		case <-deadline:
			t.Fatal("restart never completed")
		case <-time.After(time.Millisecond):
		}
	}
}

func TestRestartWaitsForActiveReplyAndPreservesQueuedInput(t *testing.T) {
	r, _, stops := restartFixture(t)
	entered := make(chan struct{})
	finish := make(chan struct{})
	r.queue.Close()
	r.queue = queue.New(t.Context(), func(ctx context.Context, _ []queue.Item) error {
		close(entered)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			t.Error("ordinary restart cancelled reply")
			return ctx.Err()
		}
	}, 1, nil, nil)
	defer r.queue.Close()
	r.notify = func(context.Context, int64, int, string) error { return nil }
	r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner"})
	<-entered
	if err := r.request(t.Context(), control.Caller{ChatID: 1, MessageID: 100}); err != nil {
		t.Fatal(err)
	}
	r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner", Text: "next"})
	r.mu.Lock()
	if *stops != 0 {
		t.Error("premature restart")
	}
	r.mu.Unlock()
	close(finish)
	waitRestart(t, r)
	if r.queue.Pending() != 1 {
		t.Fatal("queued foreground lost")
	}
}

func TestRestartDrainTimeoutDoesNotStopAndCanCancel(t *testing.T) {
	r, d, stops := restartFixture(t)
	entered := make(chan struct{})
	finish := make(chan struct{})
	blocked := make(chan struct{}, 1)
	r.queue.Close()
	r.queue = queue.New(t.Context(), func(ctx context.Context, _ []queue.Item) error {
		close(entered)
		select {
		case <-finish:
			return nil
		case <-ctx.Done():
			t.Error("timeout cancelled active reply")
			return ctx.Err()
		}
	}, 1, nil, nil)
	defer r.queue.Close()
	r.drainTimeout = 10 * time.Millisecond
	r.notify = func(_ context.Context, _ int64, _ int, text string) error {
		if strings.Contains(text, "postponed") {
			blocked <- struct{}{}
		}
		return nil
	}
	r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner"})
	<-entered
	if err := r.request(t.Context(), control.Caller{ChatID: 1, MessageID: 101}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-blocked:
	case <-time.After(time.Second):
		t.Fatal("no blocked notice")
	}
	r.mu.Lock()
	if *stops != 0 || len(d.stopped) != 0 {
		t.Error("timeout killed work")
	}
	r.mu.Unlock()
	if _, err := r.command(t.Context(), control.Caller{ChatID: 1}, "cancel"); err != nil {
		t.Fatal(err)
	}
	if r.queue.Draining() {
		t.Fatal("cancel did not reopen admission")
	}
	close(finish)
	if err := r.queue.WaitDrained(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestForceRestartCancelsDurablyAndPreservesQueuedForeground(t *testing.T) {
	r, _, _ := restartFixture(t)
	entered := make(chan struct{})
	r.queue.Close()
	r.queue = queue.New(t.Context(), func(ctx context.Context, _ []queue.Item) error { close(entered); <-ctx.Done(); return ctx.Err() }, 1, turnStoreAdapter{st: r.st}, nil)
	defer r.queue.Close()
	r.notify = func(context.Context, int64, int, string) error { return nil }
	active, _ := r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner", Source: "telegram", SourceID: "1:1"})
	<-entered
	background, _ := r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner", Source: "task", SourceID: "job:1"})
	pending, _ := r.queue.Enqueue(t.Context(), queue.Item{Folder: "owner", Source: "telegram", SourceID: "1:2"})
	if err := r.requestMode(t.Context(), control.Caller{ChatID: 1, MessageID: 102}, true); err != nil {
		t.Fatal(err)
	}
	waitRestart(t, r)
	for _, id := range []int64{active, background} {
		turn, err := r.st.GetTurn(t.Context(), id)
		if err != nil || turn.Status != store.TurnStopped {
			t.Fatal(turn, err)
		}
	}
	turn, err := r.st.GetTurn(t.Context(), pending)
	if err != nil || turn.Status != store.TurnPending {
		t.Fatal(turn, err)
	}
	fresh := queue.New(t.Context(), func(context.Context, []queue.Item) error {
		t.Error("recovery dispatched during restored barrier")
		return nil
	}, 1, turnStoreAdapter{st: r.st}, nil)
	defer fresh.Close()
	fresh.BeginDrain()
	if n, err := fresh.Recover(t.Context()); err != nil || n != 1 {
		t.Fatal(n, err)
	}
}

func TestStartedForegroundRecoveryNeverReplaysUnknownExecution(t *testing.T) {
	r, _, _ := restartFixture(t)
	id, _, err := r.st.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "1:9", ChatFolder: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	if err = r.st.MarkTurnRunning(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	items, err := (turnStoreAdapter{st: r.st}).LoadUnfinished(t.Context())
	if err != nil || len(items) != 0 {
		t.Fatal(items, err)
	}
	turn, _ := r.st.GetTurn(t.Context(), id)
	if turn.Status != store.TurnFailed || !strings.Contains(turn.Error, "execution_unknown") {
		t.Fatal(turn)
	}
}
