package runner

import (
	"context"
	"errors"
	"regexp"
	"time"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
)

const startupExecTimeout = 5 * time.Second

// StartupReport is a bounded local observation. It contains no CLI output or
// errors, credentials, remote-auth verification or model/quota readiness claim.
type StartupReport struct {
	Provider, Model, Image, ContainerID, Version string
	Container, CLI, Auth                         string
}

// StartupCheck warms only the requested trusted chat, before queue recovery.
// It never runs a model, adopts a session or mutates conversation/task state.
func (r *Runner) StartupCheck(ctx context.Context, folder string, tier Tier, allowlist *mountsec.Allowlist) (report StartupReport) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	report = StartupReport{Container: "unknown", CLI: "unknown", Auth: "unknown"}
	release, err := r.acquireAgentSlot(ctx, folder)
	if err != nil {
		report.Container = startupFailure(ctx)
		return report
	}
	defer release()
	if r.selectionBlocked(folder) {
		report.Container = "selection_unavailable"
		return report
	}
	prov, model, generation := r.startupSelection(folder)
	report.Provider, report.Model, report.Image = prov.Name(), model, prov.Image()
	defer func() { r.rememberStartupObservation(folder, report, generation) }()
	var id string
	if r.ensureFn != nil {
		id, err = r.ensure(ctx, folder, tier, allowlist)
	} else {
		id, err = r.ensureContainer(ctx, folder, tier, allowlist, true)
	}
	if err != nil {
		report.Container = startupFailure(ctx)
		return report
	}
	report.ContainerID, report.Container = id, "ready"
	r.touch(folder)
	execCtx, execCancel := context.WithTimeout(ctx, startupExecTimeout)
	result, err := r.execLocalProbe(execCtx, id, prov.VersionCmd())
	if err != nil {
		report.CLI = localProbeFailure(execCtx, err)
	} else if result.ExitCode != 0 {
		report.CLI = "error"
	} else {
		report.Version = safeStartupVersion(result.Stdout)
		if report.Version != "" {
			report.CLI = "ready"
		} else {
			report.CLI = "error"
		}
	}
	execCancel()
	if errors.Is(err, errLocalProbeCleanup) {
		return report
	}
	if ctx.Err() != nil {
		report.Auth = startupFailure(ctx)
		return report
	}
	authCtx, authCancel := context.WithTimeout(ctx, startupExecTimeout)
	defer authCancel()
	status, err := r.checkAuthViaProvider(authCtx, id, prov)
	if err != nil {
		report.Auth = localProbeFailure(authCtx, err)
	} else if status.LoggedIn {
		report.Auth = "authenticated"
	} else {
		report.Auth = "not_authenticated"
	}
	return report
}

var startupVersionPattern = regexp.MustCompile(`\b[0-9]+\.[0-9]+\.[0-9]+\b`)

func safeStartupVersion(raw string) string {
	// Only a numeric version token is retained, never arbitrary CLI stdout.
	if len(raw) > 256 {
		raw = raw[:256]
	}
	return startupVersionPattern.FindString(raw)
}
func startupFailure(ctx context.Context) string {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout"
	}
	if ctx.Err() != nil {
		return "canceled"
	}
	return "error"
}

// execLocalProbe wraps only the fixed local command; unlike execAgent it never
// restores config, mints credentials or classifies model errors recursively.
// Cancellation waits for a bounded process-group cleanup attempt before return.
func (r *Runner) execLocalProbe(ctx context.Context, id string, cmd []string) (*ExecResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	pid, err := newAgentPidFile()
	if err != nil {
		return nil, errors.New("runner: probe setup failed")
	}
	finished := make(chan bool, 1)
	cleaned := make(chan error, 1)
	go func() {
		kill := false
		select {
		case <-ctx.Done():
			kill = true
		case failed := <-finished:
			kill = failed || ctx.Err() != nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		cleanupCmd := removePidFileCmd(pid)
		if kill {
			cleanupCmd = killAgentCmd(pid, 100*time.Millisecond)
		}
		result, err := r.ex.Exec(cleanupCtx, id, cleanupCmd, nil)
		if err != nil || result == nil || result.ExitCode != 0 {
			cleaned <- errLocalProbeCleanup
			return
		}
		cleaned <- nil
	}()
	result, execErr := r.ex.Exec(ctx, id, wrapWithPidFile(pid, cmd), nil)
	finished <- execErr != nil
	if err := <-cleaned; err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if execErr != nil {
		return nil, errors.New("runner: local probe failed")
	}
	return result, nil
}

var errLocalProbeCleanup = errors.New("runner: probe cleanup failed")

func localProbeFailure(ctx context.Context, err error) string {
	if errors.Is(err, errLocalProbeCleanup) {
		return "cleanup_failed"
	}
	return startupFailure(ctx)
}

// StartupObservation is historical local evidence, never a current health probe.
// Values are copied; no container identifiers or credentials are needed by readers.
type StartupObservation struct {
	Report    StartupReport
	CheckedAt time.Time
}

func (r *Runner) startupSelection(folder string) (AgentProvider, string, uint64) {
	r.providersMu.RLock()
	defer r.providersMu.RUnlock()
	p := r.chatProviders[folder]
	if p == nil {
		p = r.provider
	}
	return p, r.chatModels[folder], r.selectionGenerations[folder]
}
func (r *Runner) invalidateStartupLocked(folder string) {
	if r.selectionGenerations == nil {
		r.selectionGenerations = make(map[string]uint64)
	}
	r.selectionGenerations[folder]++
	delete(r.startupObservations, folder)
}
func (r *Runner) rememberStartupObservation(folder string, report StartupReport, generation uint64) {
	r.providersMu.Lock()
	defer r.providersMu.Unlock()
	if r.selectionBlocks[folder] || r.selectionGenerations[folder] != generation {
		return
	}
	if r.startupObservations == nil {
		r.startupObservations = make(map[string]StartupObservation)
	}
	r.startupObservations[folder] = StartupObservation{Report: report, CheckedAt: time.Now().UTC()}
}

// CachedStartupObservation reads only the last matching startup snapshot. It
// performs no I/O, and does not assert the container still exists after idle cleanup.
func (r *Runner) CachedStartupObservation(folder string) (StartupObservation, bool) {
	r.providersMu.RLock()
	defer r.providersMu.RUnlock()
	if r.selectionBlocks[folder] {
		return StartupObservation{}, false
	}
	observation, ok := r.startupObservations[folder]
	if !ok {
		return StartupObservation{}, false
	}
	p := r.chatProviders[folder]
	if p == nil {
		p = r.provider
	}
	if p == nil || observation.Report.Provider != p.Name() || observation.Report.Image != p.Image() || observation.Report.Model != r.chatModels[folder] {
		return StartupObservation{}, false
	}
	return observation, true
}
