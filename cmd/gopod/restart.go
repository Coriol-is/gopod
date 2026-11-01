package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/errdefs"
)

const restartAnnounce = "🔄 Перезапускаю gateway и контейнеры агента. Сессии и файлы сохраняются. После запуска сообщу результат проверки."

type restartDocker interface {
	ContainerInspect(context.Context, string) (container.InspectResponse, error)
	ContainerStop(context.Context, string, container.StopOptions) error
	ContainerList(context.Context, container.ListOptions) ([]container.Summary, error)
}

type restartNotice func(context.Context, int64, int, string) error

type controllerRestart struct {
	st           *store.Store
	docker       restartDocker
	chatsDir     string
	ownerID      int64
	stop         context.CancelFunc
	notify       restartNotice
	log          *slog.Logger
	hostname     string
	mu           sync.Mutex
	requested    atomic.Bool
	queue        *queue.Queue
	runtime      restartRuntime
	app          context.Context
	drainCancel  context.CancelFunc
	drainTimeout time.Duration
}

type restartRuntime interface {
	BeginDrain()
	CancelDrain()
	WaitDrained(context.Context) error
	ForceDrain(context.Context) error
	CancelActive()
}

func (r *controllerRestart) request(ctx context.Context, caller control.Caller) error {
	return r.requestMode(ctx, caller, false)
}
func (r *controllerRestart) requestMode(ctx context.Context, caller control.Caller, force bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if r.docker == nil || r.stop == nil || r.queue == nil || r.runtime == nil {
		return errors.New("Restart unavailable: drain coordinator is not ready.")
	}
	if e := r.supervised(ctx); e != nil {
		return e
	}
	// Preflight authority, not the inventory used to stop containers after drain.
	targets, e := r.snapshotFor(ctx, true)
	if e != nil {
		return e
	}
	mode := "drain"
	if force {
		mode = "force"
	}
	record := store.RestartRecord{MessageID: caller.MessageID, ChatID: caller.ChatID, ThreadID: caller.ThreadID, Containers: targets, Mode: mode}
	if e = r.st.BeginRestart(ctx, record); e != nil {
		return e
	}
	text := "⏳ Restart requested. Waiting for active replies and tasks to finish. New messages remain queued. Use /restart status or /restart cancel."
	if force {
		text = "⛔ Forced restart requested. Active replies and background work will stop; queued background work is cancelled. Schedules and files are preserved."
	}
	if e = r.notify(ctx, caller.ChatID, caller.ThreadID, text); e != nil {
		_ = r.abortUnstarted(caller.MessageID, "notice_unknown")
		return e
	}
	// LockTasks prevents a scheduler enqueue crossing the force cutoff.
	unlock := scheduler.LockTasks(r.st)
	r.queue.BeginDrain()
	phase := "draining"
	if force {
		phase = "forcing"
	}
	if e = r.st.SetRestartPhase(ctx, caller.MessageID, "requested", phase); e != nil {
		r.queue.CancelDrain()
		unlock()
		_ = r.abortUnstarted(caller.MessageID, "aborted")
		return e
	}
	if force {
		e = r.queue.ForceDrain(ctx, func(items []queue.Item) error {
			ids := make([]int64, 0, len(items))
			for _, it := range items {
				rc, e := r.st.GetByFolder(ctx, it.Folder)
				if e != nil || !rc.IsOwner {
					return errors.New("Force restart refused: non-personal queued work needs its own procedure.")
				}
				ids = append(ids, it.ID)
			}
			return r.st.RecordForcedTurns(ctx, caller.MessageID, ids)
		})
		if e != nil {
			unlock()
			r.finishPhase(caller.MessageID, phase, "aborted")
			r.queue.CancelDrain()
			return e
		}
	}
	unlock()
	base := r.app
	if base == nil {
		base = context.Background()
	}
	dctx, dcancel := context.WithCancel(base)
	r.drainCancel = dcancel
	go r.completeDrain(dctx, record, force)
	return nil
}

func (r *controllerRestart) completeDrain(ctx context.Context, record store.RestartRecord, force bool) {
	timeout := r.drainTimeout
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	phase := "draining"
	if force {
		phase = "forcing"
	}
	var err error
	if force {
		r.runtime.CancelActive()
		var currentTargets []store.RestartContainer
		currentTargets, err = r.snapshotFor(ctx, true)
		if err == nil {
			err = r.st.UpdateRestartTargets(ctx, record.MessageID, currentTargets)
		}
		if err == nil {
			record.Containers = currentTargets
		}
		// Cancellation intent is durable. Stop exact authorized containers to include
		// detached child tools, then let runner verify all operation claims settled.
		for _, target := range record.Containers {
			if err != nil {
				break
			}
			if err = r.stopTarget(ctx, target); err != nil {
				break
			}
		}
		if err == nil {
			err = r.runtime.ForceDrain(ctx)
		}
		if err == nil {
			err = r.queue.WaitDrained(ctx)
		}
		if err == nil {
			err = r.settleForced(ctx, record.MessageID)
		}
	} else {
		// Let all already dispatched queue handlers enter the runner and finish before
		// closing auxiliary admission. Closing runner first can reject such a handler.
		err = r.queue.WaitDrained(ctx)
		if err == nil {
			r.mu.Lock()
			if ctx.Err() != nil {
				err = ctx.Err()
			} else {
				r.runtime.BeginDrain()
			}
			r.mu.Unlock()
			if err == nil {
				err = r.runtime.WaitDrained(ctx)
			}
		}
	}
	if err == nil {
		var targets []store.RestartContainer
		targets, err = r.snapshot(ctx)
		if err == nil {
			err = r.st.UpdateRestartTargets(ctx, record.MessageID, targets)
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	current, readErr := r.st.ReadRestart(context.WithoutCancel(ctx))
	if readErr != nil || current == nil || current.MessageID != record.MessageID || current.Phase != phase {
		return
	}
	if err != nil {
		r.finishPhase(record.MessageID, phase, "blocked")
		nctx, ncancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ncancel()
		text := "Restart postponed: active work or termination evidence remains unresolved. Nothing is forcibly stopped by a normal restart. Use /restart status or /restart cancel."
		if force {
			text = "Forced cancellation was requested, but restart did not complete. Execution may be unknown; automatic replay is blocked. Use /restart status."
		}
		_ = r.notify(nctx, record.ChatID, record.ThreadID, text)
		return
	}
	if err = r.st.SetRestartPhase(ctx, record.MessageID, phase, "restarting"); err != nil {
		r.finishPhase(record.MessageID, phase, "blocked")
		nctx, ncancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer ncancel()
		_ = r.notify(nctx, record.ChatID, record.ThreadID, "Restart blocked: final durable state could not be committed. No shutdown requested.")
		return
	}
	r.requested.Store(true)
	r.stop()
}

func (r *controllerRestart) command(ctx context.Context, caller control.Caller, action string) (string, error) {
	action, expected, _ := strings.Cut(action, ":")
	switch action {
	case "":
		return "", r.requestMode(ctx, caller, false)
	case "force":
		if resumed, err := r.retryForce(ctx, caller); resumed || err != nil {
			return "Resuming the recorded forced restart; no new cancellation cutoff was taken.", err
		}
		return "", r.requestMode(ctx, caller, true)
	case "status":
		rec, e := r.st.ReadRestart(ctx)
		if e != nil {
			return "", e
		}
		if rec == nil {
			return "No restart pending.", nil
		}
		if expected != "" && strconv.Itoa(rec.MessageID) != expected {
			return "This restart control is stale.", nil
		}
		return fmt.Sprintf("Restart %d: %s (%s).", rec.MessageID, rec.Phase, rec.Mode), nil
	case "cancel":
		r.mu.Lock()
		defer r.mu.Unlock()
		rec, e := r.st.ReadRestart(ctx)
		if e != nil {
			return "", e
		}
		if rec != nil && expected != "" && strconv.Itoa(rec.MessageID) != expected {
			return "This restart control is stale.", nil
		}
		if rec == nil || rec.Mode == "force" || (rec.Phase != "draining" && rec.Phase != "blocked") {
			return "Restart can no longer be cancelled.", nil
		}
		if e = r.st.SetRestartPhase(ctx, rec.MessageID, rec.Phase, "cancelled"); e != nil {
			return "", e
		}
		if r.drainCancel != nil {
			r.drainCancel()
		}
		r.runtime.CancelDrain()
		r.queue.CancelDrain()
		return "Restart cancelled; queued work may continue.", nil
	}
	return "", errors.New("Use /restart [force|status|cancel].")
}

func (r *controllerRestart) supervised(ctx context.Context) error {
	if len(r.hostname) < 12 {
		return errors.New("Restart unavailable outside the supervised Docker controller.")
	}
	c, err := r.docker.ContainerInspect(ctx, r.hostname)
	if err != nil || !restartSupervisor(c, r.hostname, time.Now()) {
		return errors.New("Restart unavailable: controller identity, restart policy or startup grace could not be verified.")
	}
	return nil
}

func restartSupervisor(c container.InspectResponse, hostname string, now time.Time) bool {
	if c.ContainerJSONBase == nil || c.Config == nil || c.HostConfig == nil || c.State == nil {
		return false
	}
	if !strings.HasPrefix(c.ID, hostname) || c.Config.Hostname != hostname || c.Config.Labels["com.docker.compose.service"] != "gopod" || c.Config.Labels[runner.LabelChat] != "" {
		return false
	}
	policy := c.HostConfig.RestartPolicy.Name
	started, err := time.Parse(time.RFC3339Nano, c.State.StartedAt)
	return (policy == "always" || policy == "unless-stopped") && c.State.Running && !c.State.Paused && err == nil && now.Sub(started) >= 10*time.Second
}

func (r *controllerRestart) snapshot(ctx context.Context) ([]store.RestartContainer, error) {
	return r.snapshotFor(ctx, false)
}
func (r *controllerRestart) snapshotFor(ctx context.Context, allowHeld bool) ([]store.RestartContainer, error) {
	holds, err := r.st.HeldTaskReservations(ctx)
	if err != nil || (len(holds) > 0 && !allowHeld) {
		return nil, errors.New("Restart refused while managed task exit evidence is unresolved.")
	}
	list, err := r.docker.ContainerList(ctx, container.ListOptions{All: true})
	if err != nil {
		return nil, errors.New("Restart unavailable: container inventory failed.")
	}
	var targets []store.RestartContainer
	for _, c := range list {
		target, err := r.personalTarget(ctx, c)
		if err != nil {
			return nil, err
		}
		if target != nil {
			targets = append(targets, *target)
		}
	}
	return targets, nil
}

func (r *controllerRestart) personalTarget(ctx context.Context, c container.Summary) (*store.RestartContainer, error) {
	folder := c.Labels[runner.LabelChat]
	if folder == "" || c.State != "running" {
		return nil, nil
	}
	sc, err := r.st.SnapshotScope(ctx, folder)
	if errors.Is(err, store.ErrStaleScope) {
		return nil, nil // unrelated controller/retired namespace
	}
	if err != nil {
		return nil, errors.New("Restart unavailable: scope lookup failed.")
	}
	personal := sc.JID == fmt.Sprintf("tg:%d", r.ownerID)
	if !personal && c.Labels["gopod.group-native"] == sc.NamespaceID {
		return nil, errors.New("Restart refused: an active group runtime requires its own recovery procedure.")
	}
	info, err := r.docker.ContainerInspect(ctx, c.ID)
	if err != nil {
		return nil, errors.New("Restart unavailable: container inspection failed.")
	}
	if !restartWorkspace(info, filepath.Join(r.chatsDir, folder)) {
		return nil, nil
	}
	if !personal {
		return nil, errors.New("Restart refused: an active non-personal runtime requires its own recovery procedure.")
	}
	return &store.RestartContainer{ID: c.ID, Scope: sc}, nil
}

func restartWorkspace(c container.InspectResponse, want string) bool {
	for _, m := range c.Mounts {
		if m.Destination == "/workspace/chat" && filepath.Clean(m.Source) == filepath.Clean(want) {
			return true
		}
	}
	return false
}

// recycle runs only with the controller instance lock held, before startup
// cleanup/warmup (or after shutdown drains). Stopping exact old IDs is idempotent;
// a crash cannot accidentally stop the newly-created replacement container.
func (r *controllerRestart) recycle(ctx context.Context) error {
	record, err := r.st.ReadRestart(ctx)
	if err != nil || record == nil || record.Phase != "restarting" {
		return err
	}
	holds, err := r.st.HeldTaskReservations(ctx)
	if err != nil || len(holds) > 0 {
		return errors.New("restart blocked by unresolved managed task evidence")
	}
	for _, target := range record.Containers {
		if err = r.stopTarget(ctx, target); err != nil {
			return err
		}
	}
	return nil
}

func (r *controllerRestart) stopTarget(ctx context.Context, target store.RestartContainer) error {
	if err := r.st.ValidateScope(ctx, target.Scope); err != nil {
		return fmt.Errorf("restart scope changed: %w", err)
	}
	info, err := r.docker.ContainerInspect(ctx, target.ID)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restart inspect: %w", err)
	}
	if info.Config == nil || info.Config.Labels[runner.LabelChat] != target.Scope.Folder || !restartWorkspace(info, filepath.Join(r.chatsDir, target.Scope.Folder)) {
		return errors.New("restart container identity changed")
	}
	if info.State != nil && !info.State.Running {
		return nil
	}
	grace := 5
	if err = r.docker.ContainerStop(ctx, target.ID, container.StopOptions{Timeout: &grace}); err != nil {
		return fmt.Errorf("restart stop: %w", err)
	}
	info, err = r.docker.ContainerInspect(ctx, target.ID)
	if errdefs.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("restart verify stopped: %w", err)
	}
	if info.State == nil || info.State.Running {
		return errors.New("restart stop: container exit unproven")
	}
	return nil
}

func (r *controllerRestart) finishPhase(id int, from, to string) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.st.SetRestartPhase(ctx, id, from, to); err != nil {
		r.log.Warn("restart state not updated", slog.Any("err", err))
	}
}

// recovered claims the notice before sending: a crash/timeout never produces a
// success-notification replay loop. It never requests another controller restart.
func (r *controllerRestart) recovered(ctx context.Context, agent *runner.Runner, recoveryErr error) bool {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	record, err := r.st.ReadRestart(ctx)
	if err != nil {
		r.log.Warn("restart record unavailable", slog.Any("err", err))
		return true
	}
	if record == nil || (record.Phase != "restarting" && record.Phase != "requested") {
		return false
	}
	ready := record.Phase == "restarting" && recoveryErr == nil && agent != nil
	if ready {
		ready = r.ownerReady(ctx, agent)
	}
	text := "⚠️ Gateway запущен, но восстановление контейнеров или локальная проверка CLI/auth не подтверждены. Используйте /telegram doctor. Автоматического повторного рестарта не будет."
	phase := "degraded"
	if ready {
		phase = "ready"
		text = "✅ Gateway перезапущен. Контейнер агента работает; CLI и локальное состояние входа проверены. Проверка ответа модели и доступной квоты не выполнялась."
	}
	if err = r.st.SetRestartPhase(ctx, record.MessageID, record.Phase, "notice_claimed"); err != nil {
		r.log.Warn("restart notice not claimed", slog.Any("err", err))
		return true
	}
	if err = r.notify(ctx, record.ChatID, record.ThreadID, text); err != nil {
		phase = "notice_unknown"
		r.log.Warn("restart recovery notice not acknowledged", slog.Any("err", err))
	}
	r.finishPhase(record.MessageID, "notice_claimed", phase)
	return true
}

func (r *controllerRestart) ownerReady(ctx context.Context, agent *runner.Runner) bool {
	chat, err := r.st.GetRegistered(ctx, fmt.Sprintf("tg:%d", r.ownerID))
	if err != nil {
		return false
	}
	observation, ok := agent.CachedStartupObservation(chat.Folder)
	if !ok || observation.Report.Container != "ready" || observation.Report.CLI != "ready" || observation.Report.Auth != "authenticated" {
		return false
	}
	info, err := r.docker.ContainerInspect(ctx, observation.Report.ContainerID)
	if err != nil || !restartRuntimeReady(info, observation.Report, chat.Folder, filepath.Join(r.chatsDir, chat.Folder)) {
		return false
	}
	current, ok := agent.CachedStartupObservation(chat.Folder)
	return ok && current.CheckedAt.Equal(observation.CheckedAt)
}

func newControllerRestart(st *store.Store, chatsDir string, ownerID int64, stop context.CancelFunc, log *slog.Logger) *controllerRestart {
	hostname, _ := os.Hostname()
	return &controllerRestart{st: st, chatsDir: chatsDir, ownerID: ownerID, stop: stop, hostname: hostname, log: log}
}

func restartRuntimeReady(info container.InspectResponse, report runner.StartupReport, folder, workspace string) bool {
	if info.ContainerJSONBase == nil || info.State == nil || info.Config == nil || !info.State.Running || info.State.Paused {
		return false
	}
	if info.Config.Image != report.Image || info.Config.Labels[runner.LabelChat] != folder || !restartWorkspace(info, workspace) {
		return false
	}
	role := info.Config.Labels[runner.LabelPolicyRole]
	return role == "" || role == string(runner.RoleForegroundPersonal)
}

func (r *controllerRestart) abortUnstarted(id int, terminal string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var err error
	for attempt := 0; attempt < 3; attempt++ {
		err = r.st.AbortRestart(ctx, id, terminal)
		if err == nil || errors.Is(err, store.ErrRestartReplay) {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	r.log.Warn("unstarted restart receipt repair failed", slog.Any("err", err))
	return err
}

// restoreDrain preserves an interrupted control operation's admission barrier.
// Recovery does not silently reissue termination; explicit force retry reconciles it.
func (r *controllerRestart) restoreDrain(ctx context.Context) error {
	rec, err := r.st.ReadRestart(ctx)
	if err != nil {
		return err
	}
	if rec == nil {
		return nil
	}
	switch rec.Phase {
	case "requested", "draining", "forcing", "blocked":
		r.queue.BeginDrain()
		r.runtime.BeginDrain()
		if rec.Phase != "blocked" {
			return r.st.SetRestartPhase(ctx, rec.MessageID, rec.Phase, "blocked")
		}
	}
	return nil
}
func (r *controllerRestart) retryForce(ctx context.Context, caller control.Caller) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	rec, err := r.st.ReadRestart(ctx)
	if err != nil {
		return false, err
	}
	if rec == nil || rec.Mode != "force" || rec.Phase != "blocked" {
		return false, nil
	}
	if r.queue == nil || r.runtime == nil {
		return true, errors.New("Restart drain coordinator unavailable.")
	}
	if err = r.supervised(ctx); err != nil {
		return true, err
	}
	if err = r.st.SetRestartPhase(ctx, rec.MessageID, "blocked", "forcing"); err != nil {
		return true, err
	}
	base := r.app
	if base == nil {
		base = context.Background()
	}
	go r.completeDrain(base, *rec, true)
	return true, nil
}
func (r *controllerRestart) settleForced(ctx context.Context, id int) error {
	rec, err := r.st.ReadRestart(ctx)
	if err != nil {
		return err
	}
	if rec == nil || rec.MessageID != id {
		return store.ErrRestartReplay
	}
	for _, turnID := range rec.ForceTurns {
		turn, e := r.st.GetTurn(ctx, turnID)
		if e != nil {
			return e
		}
		if turn.AttemptID == "" {
			continue
		}
		a, e := r.st.GetTaskAttempt(ctx, turn.AttemptID)
		if e != nil {
			return e
		}
		if a.State == store.ExecCancelRequested || a.State == store.ExecExecutionUnknown || a.State == store.ExecRunning {
			if _, e = r.st.SettleTaskAttempt(ctx, a.ID, store.Settlement{Kind: store.SettleStopped, Evidence: "local_exit:forced_restart_container_stopped", FailureCode: "forced_restart"}); e != nil {
				return e
			}
		}
		if a.Reservation == "held" {
			if e = r.queue.ReleaseHold(ctx, a.ID, queue.ProofLocalExit, "forced restart: scoped container stopped"); e != nil {
				return e
			}
		}
		if e = r.st.MarkTurnFinished(ctx, turnID, store.TurnStopped, "forced restart: execution terminated"); e != nil {
			return e
		}
	}
	return r.st.SettleForcedTurns(ctx, id)
}
