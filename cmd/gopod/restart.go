package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/runner"
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
	st        *store.Store
	docker    restartDocker
	chatsDir  string
	ownerID   int64
	stop      context.CancelFunc
	notify    restartNotice
	log       *slog.Logger
	hostname  string
	mu        sync.Mutex
	requested atomic.Bool
}

func (r *controllerRestart) request(ctx context.Context, caller control.Caller) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if r.docker == nil || r.stop == nil {
		return errors.New("Restart unavailable: Docker controller is not ready.")
	}
	if err := r.supervised(ctx); err != nil {
		return err
	}
	targets, err := r.snapshot(ctx)
	if err != nil {
		return err
	}
	if !r.requested.Load() {
		previous, readErr := r.st.ReadRestart(ctx)
		if readErr != nil {
			return errors.New("Restart state is unavailable; nothing was stopped.")
		}
		if previous != nil && time.Now().UnixMilli()-previous.CreatedAt >= time.Minute.Milliseconds() && (previous.Phase == "requested" || previous.Phase == "restarting") {
			if err := r.abortUnstarted(previous.MessageID, "aborted"); err != nil {
				return errors.New("Unstarted restart state could not be repaired; nothing was stopped.")
			}
		}
	}
	record := store.RestartRecord{MessageID: caller.MessageID, ChatID: caller.ChatID, ThreadID: caller.ThreadID, Containers: targets}
	if err = r.st.BeginRestart(ctx, record); err != nil {
		if errors.Is(err, store.ErrRestartReplay) {
			return errors.New("This restart command was already handled; it will not run again.")
		}
		if errors.Is(err, store.ErrRestartPending) {
			return errors.New("Restart is already pending or cooling down. Wait one minute.")
		}
		return errors.New("Restart was not recorded; nothing was stopped.")
	}
	if err = r.notify(ctx, caller.ChatID, caller.ThreadID, restartAnnounce); err != nil {
		_ = r.abortUnstarted(caller.MessageID, "notice_unknown")
		return errors.New("The restart notice was not acknowledged. Restart was not started; check the chat before submitting a new command.")
	}
	if err = r.st.SetRestartPhase(ctx, caller.MessageID, "requested", "restarting"); err != nil {
		_ = r.abortUnstarted(caller.MessageID, "aborted")
		return errors.New("Restart state could not be committed; no shutdown requested.")
	}
	r.requested.Store(true)
	r.stop() // graceful app drain, then normal process exit; verified Docker policy restarts it
	return nil
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
	holds, err := r.st.HeldTaskReservations(ctx)
	if err != nil || len(holds) > 0 {
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
	if err != nil {
		return nil, nil
	} // unrelated controller/retired namespace
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
