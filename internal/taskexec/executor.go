// Package taskexec is the managed task handler wired into queue.ManagedRuntime
// (CP-004 plan PR 3). It joins the runner's RunTaskStream to the PR 2 ledger
// contract: snapshot inside the execution slot, spawn marker before native
// exec, exit evidence only from the daemon, and an attempt-bound report grant
// revoked when the attempt ends. It never treats a nil error or a model report
// as proof of anything.
package taskexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/taskreport"
)

// Runner is the slice of *runner.Runner the executor uses.
type Runner interface {
	ResolveTaskSnapshot(ctx context.Context, folder string) (runner.TaskSnapshot, error)
	RunTaskStream(ctx context.Context, req runner.TaskRunRequest) (*runner.AgentStream, error)
}

// ReportStore persists provisional reports (satisfied by *store.Store).
type ReportStore interface {
	RecordProvisionalReport(ctx context.Context, r store.ProvisionalReport) error
	SnapshotScope(ctx context.Context, folder string) (store.ScopeRef, error)
}

// Executor runs managed task attempts.
type Executor struct {
	Runner  Runner
	Grants  *taskreport.Grants
	Reports ReportStore
	DataDir string // IPC namespaces live under <DataDir>/ipc/<folder>
	Timeout time.Duration
	Log     *slog.Logger
	// SweepEvery is the live report poll interval (0 = 10s).
	SweepEvery time.Duration
}

// Snapshot is queue.ManagedRuntime.Snapshot.
func (e *Executor) Snapshot(ctx context.Context, it queue.Item) (queue.Snapshot, error) {
	s, err := e.Runner.ResolveTaskSnapshot(ctx, it.Folder)
	if err != nil {
		return queue.Snapshot{}, mapPreSpawn(err)
	}
	return queue.Snapshot{Provider: s.Provider, Model: s.Model, ProfileFingerprint: s.ProfileFingerprint}, nil
}

// mapPreSpawn classifies a failure that happened before the spawn marker.
// Deterministic refusals are denials; container-guard conditions an operator or
// the passage of time can clear are retryable not-started failures.
func mapPreSpawn(err error) error {
	switch {
	case errors.Is(err, runner.ErrTaskDenied), errors.Is(err, runner.ErrTaskRoleUnqualified),
		errors.Is(err, runner.ErrManagedTasksDisabled):
		return fmt.Errorf("%w: %v", queue.ErrDenied, err)
	case errors.Is(err, runner.ErrPolicyUnknown), errors.Is(err, runner.ErrExecutionUnproven),
		errors.Is(err, runner.ErrRecycleNeedsSlot):
		return fmt.Errorf("%w: %v", queue.ErrNotStarted, err)
	}
	return err
}

// Handle is queue.ManagedRuntime.Handler. A managed batch is always one item.
func (e *Executor) Handle(ctx context.Context, items []queue.Item) error {
	if len(items) != 1 {
		return fmt.Errorf("%w: managed batch of %d", queue.ErrDenied, len(items))
	}
	it := items[0]
	ex, ok := queue.ExecutionFrom(ctx)
	if !ok {
		return fmt.Errorf("%w: no execution context", queue.ErrDenied)
	}
	snap := ex.Snapshot()

	scope, err := e.Reports.SnapshotScope(ctx, it.Folder)
	if err != nil {
		return fmt.Errorf("%w: scope: %v", queue.ErrNotStarted, err)
	}
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = runner.DefaultTaskTimeout
	}
	tok, err := e.Grants.Issue(taskreport.Binding{Folder: it.Folder, RunID: it.RunID, AttemptID: it.AttemptID, Scope: scope}, timeout+time.Minute)
	if err != nil {
		return fmt.Errorf("%w: report grant: %v", queue.ErrDenied, err)
	}
	defer e.Grants.Revoke(it.AttemptID) // settlement or cancellation ends the capability

	dir := filepath.Join(e.DataDir, "ipc", it.Folder, taskreport.DirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("%w: report dir: %v", queue.ErrNotStarted, err)
	}
	sw := &taskreport.Sweeper{Grants: e.Grants, Dir: dir, Folder: it.Folder,
		Record: func(c context.Context, p taskreport.Provisional) error {
			return e.Reports.RecordProvisionalReport(c, store.ProvisionalReport{
				RunID: p.RunID, AttemptID: p.AttemptID, Status: p.Status,
				OutcomeJSON: p.OutcomeJSON, CheckpointJSON: p.CheckpointJSON, PlanVersion: p.PlanVersion,
			})
		}}

	began := false
	req := runner.TaskRunRequest{
		Folder:       it.Folder,
		Prompt:       it.Text,
		SystemPrompt: systemPrompt(),
		Timeout:      timeout,
		Snapshot:     runner.TaskSnapshot{Provider: snap.Provider, Model: snap.Model, ProfileFingerprint: snap.ProfileFingerprint},
		Env: []string{
			"GOPOD_TASK_RUN_ID=" + it.RunID,
			"GOPOD_TASK_ATTEMPT_ID=" + it.AttemptID,
			"GOPOD_TASK_REPORT_TOKEN=" + tok,
		},
		Hooks: runner.TaskHooks{
			BeginSpawn: func(c context.Context) error {
				err := ex.BeginSpawn(c)
				began = err == nil
				return err
			},
			Exited: ex.Exited,
		},
	}
	stream, err := e.Runner.RunTaskStream(ctx, req)
	if err != nil {
		if began || errors.Is(err, queue.ErrSpawnRefused) {
			return err // the marker may be committed: never relabel as not started
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		err = mapPreSpawn(err)
		if !errors.Is(err, queue.ErrDenied) && !errors.Is(err, queue.ErrNotStarted) {
			err = fmt.Errorf("%w: %v", queue.ErrNotStarted, err) // failed before the marker
		}
		return err
	}

	// Stdout is discarded (D038): delivery is by agent IPC only.
	every := e.SweepEvery
	if every <= 0 {
		every = 10 * time.Second
	}
	tick := time.NewTicker(every)
	defer tick.Stop()
	bytesOut := 0
	var res runner.RunResult
loop:
	for {
		select {
		case c, ok := <-stream.Chunks:
			if !ok {
				break loop
			}
			bytesOut += len(c)
		case <-tick.C:
			sw.Sweep(ctx)
		}
	}
	res = <-stream.Result
	// Final sweep while the capability is still live, then revoke (deferred).
	sw.Sweep(context.WithoutCancel(ctx))
	e.Log.Info("managed task attempt finished; stdout discarded",
		slog.String("run", it.RunID), slog.String("attempt", it.AttemptID), slog.Int("stdout_bytes", bytesOut), slog.Bool("failed", res.Err != nil))
	return res.Err
}

// systemPrompt is trusted run instruction text: no foreground history, no
// compiled memory. The report channel is a claim, not completion.
func systemPrompt() string {
	return `You are running a scheduled task with no user present. Deliver any message the task asks for through the IPC messages directory; your stdout is discarded.
Optionally record a self-assessment by writing one JSON file (version 1, at most 32 KiB) into /workspace/ipc/task-results/ with fields version, runId (GOPOD_TASK_RUN_ID), attemptId (GOPOD_TASK_ATTEMPT_ID), runToken (GOPOD_TASK_REPORT_TOKEN), outcome {status: achieved|partial|blocked|unknown, checks, evidence, limitations (max 8 short strings each)} and checkpoint {planVersion, lastSupportedMilestone, nextSafeStep, unresolvedEffects} or null. It is your own claim and is never treated as verification.`
}
