package runner

// Managed task execution (CP-004 plan PR 3, D044).
//
// RunTaskStream is not Run/RunStream and not RunFresh. It never resolves,
// adopts, creates or records a foreground session, never builds a handoff,
// never compiles or extracts memory, never counts toward auto-compact, and
// writes nothing to the session store. It is not the one-minute maintenance
// path either: a foreground waiter does not cancel it. It runs under the
// ordinary bounded task lifetime.
//
// The only authority it uses is the frozen attempt snapshot (provider, model and
// profile fingerprint) and the host-derived task-role policy. The caller commits
// the spawn marker (Hooks.BeginSpawn) before the runner requests native
// execution, and learns of an exit only through Hooks.Exited, which is called
// solely when the daemon reported the exec finished.

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
)

// Task lifetime bounds (CP-004: default 5 minutes, approved maximum 60).
const (
	DefaultTaskTimeout = 5 * time.Minute
	MaxTaskTimeout     = 60 * time.Minute

	// maxTaskArgBytes bounds prompt plus system prompt: both travel as a single
	// exec argument and Linux caps one argument at 128 KiB.
	maxTaskArgBytes = 96 << 10

	// defaultTaskExitWait is how long a killed run is given to be reported as
	// exited by the daemon before the attachment is dropped (no proof then).
	defaultTaskExitWait = 10 * time.Second

	taskTouchInterval = time.Minute
)

// TaskSnapshot is the provider selection frozen on an attempt.
type TaskSnapshot struct {
	Provider           string
	Model              string
	ProfileFingerprint string
}

// TaskHooks connect one run to the task ledger.
type TaskHooks struct {
	// BeginSpawn persists the spawn marker. Required. Native execution is never
	// requested unless it returns nil.
	BeginSpawn func(ctx context.Context) error
	// Exited records host-observed exit evidence. Required. Never called for a
	// closed attachment, a timeout or a kill request alone.
	Exited func(evidence string)
}

// TaskRunRequest is one managed task execution.
type TaskRunRequest struct {
	Folder string
	// Prompt is the task prompt; SystemPrompt trusted host-composed run
	// instructions. Neither includes foreground history or compiled memory.
	Prompt       string
	SystemPrompt string
	// Timeout is the run lifetime: 0 means DefaultTaskTimeout, above
	// MaxTaskTimeout is refused.
	Timeout  time.Duration
	Snapshot TaskSnapshot
	// Env is extra per-exec environment composed by the host (report grant).
	Env   []string
	Hooks TaskHooks
}

func (q TaskRunRequest) validate() (time.Duration, error) {
	switch {
	case q.Folder == "":
		return 0, fmt.Errorf("%w: empty folder", ErrTaskDenied)
	case strings.TrimSpace(q.Prompt) == "":
		return 0, fmt.Errorf("%w: empty prompt", ErrTaskDenied)
	case len(q.Prompt)+len(q.SystemPrompt) > maxTaskArgBytes:
		return 0, fmt.Errorf("%w: prompt too large", ErrTaskDenied)
	case q.Snapshot.Provider == "" || q.Snapshot.ProfileFingerprint == "":
		return 0, fmt.Errorf("%w: attempt snapshot incomplete", ErrTaskDenied)
	case q.Hooks.BeginSpawn == nil || q.Hooks.Exited == nil:
		return 0, fmt.Errorf("%w: ledger hooks required", ErrTaskDenied)
	case q.Timeout < 0 || q.Timeout > MaxTaskTimeout:
		return 0, fmt.Errorf("%w: timeout outside 0..%s", ErrTaskDenied, MaxTaskTimeout)
	}
	if q.Timeout == 0 {
		return DefaultTaskTimeout, nil
	}
	return q.Timeout, nil
}

// providerNamed resolves a snapshot's provider by name. The chat's current
// selection is consulted only to find the object, never to choose the provider.
func (r *Runner) providerNamed(folder, name string) AgentProvider {
	if p := r.ProviderForChat(folder); p != nil && p.Name() == name {
		return p
	}
	if r.provider != nil && r.provider.Name() == name {
		return r.provider
	}
	if r.managed != nil {
		return r.managed.providers[name]
	}
	return nil
}

// ResolveTaskSnapshot resolves the provider, model and profile fingerprint for
// the next attempt in folder. Call it once the folder's slot is held and before
// the reservation commits; the result is frozen on the attempt and a later
// change of the chat's provider or model never reaches a started attempt.
func (r *Runner) ResolveTaskSnapshot(ctx context.Context, folder string) (TaskSnapshot, error) {
	if r.managed == nil {
		return TaskSnapshot{}, ErrManagedTasksDisabled
	}
	if err := r.authorizeOwner(ctx, folder); err != nil {
		return TaskSnapshot{}, fmt.Errorf("%w: %v", ErrTaskDenied, err)
	}
	if _, group, err := r.groupRegistration(ctx, folder); err != nil || group {
		return TaskSnapshot{}, fmt.Errorf("%w: group tasks are disabled", ErrTaskDenied)
	}
	prov := r.ProviderForChat(folder)
	if _, ok := prov.(TaskCommandProvider); !ok {
		return TaskSnapshot{}, fmt.Errorf("%w: provider has no task command", ErrTaskDenied)
	}
	plan, err := r.planFor(ctx, folder, RoleScheduledTaskPersonal, prov, nil)
	if err != nil {
		if errors.Is(err, ErrTaskRoleUnqualified) {
			return TaskSnapshot{}, fmt.Errorf("%w: %v", ErrTaskDenied, err)
		}
		return TaskSnapshot{}, err
	}
	return TaskSnapshot{
		Provider:           prov.Name(),
		Model:              r.ModelForChat(folder),
		ProfileFingerprint: plan.Policy.Fingerprint(),
	}, nil
}

// RunTaskStream runs one managed task attempt. Every error it returns before the
// spawn marker is a pre-spawn denial or failure (wrapping ErrTaskDenied, or a
// context error); an error from Hooks.BeginSpawn is returned unchanged so the
// queue can tell a refused marker from an ambiguous one. After the marker, the
// outcome arrives on the stream's Result.
func (r *Runner) RunTaskStream(ctx context.Context, req TaskRunRequest) (*AgentStream, error) {
	if r.managed == nil {
		return nil, ErrManagedTasksDisabled
	}
	timeout, err := req.validate()
	if err != nil {
		return nil, err
	}
	folder := req.Folder
	if err := r.authorizeOwner(ctx, folder); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrTaskDenied, err)
	}
	if _, group, err := r.groupRegistration(ctx, folder); err != nil || group {
		return nil, fmt.Errorf("%w: group tasks are disabled", ErrTaskDenied)
	}
	prov := r.providerNamed(folder, req.Snapshot.Provider)
	if prov == nil {
		return nil, fmt.Errorf("%w: snapshot provider unavailable", ErrTaskDenied)
	}
	tp, ok := prov.(TaskCommandProvider)
	if !ok {
		return nil, fmt.Errorf("%w: provider has no task command", ErrTaskDenied)
	}

	releaseSlot, err := r.acquireAgentSlot(ctx, folder)
	if err != nil {
		return nil, err
	}
	ctx, finishSetup := r.operationContext(ctx)
	originalRelease := releaseSlot
	releaseSlot = func() { finishSetup(); originalRelease() }
	started := false
	defer func() {
		if !started {
			releaseSlot()
		}
	}()

	id, err := r.ensureRole(ctx, folder, RoleScheduledTaskPersonal, prov, nil, false, true, req.Snapshot.ProfileFingerprint)
	if err != nil {
		return nil, err
	}
	releaseGate, err := r.admit(ctx, folder, ClassInteractive)
	if err != nil {
		return nil, err
	}
	gateHeld := true
	defer func() {
		if gateHeld {
			r.releaseGateWhenSettled(ctx, folder, releaseGate)
		}
	}()

	cmd := withModel(tp.RunTaskCmd(req.Prompt, req.SystemPrompt), prov, req.Snapshot.Model)
	if restore := prov.RestoreConfigCmd(); restore != nil {
		r.ex.Exec(ctx, id, restore, nil)
	}
	pidFile, err := newAgentPidFile()
	if err != nil {
		return nil, err
	}
	env := slices.Clone(req.Env)
	if r.nodeHeapMB > 0 && prov.Name() == "claude" {
		env = withNodeHeap(env, "", r.nodeHeapMB)
	}

	// Spawn marker: after this returns nil, the attempt may have started.
	if err := req.Hooks.BeginSpawn(ctx); err != nil {
		return nil, err
	}

	opCtx, finishOperation := r.operationContext(ctx)
	tctx, cancel := context.WithTimeout(opCtx, timeout)
	handle, err := r.ex.ExecStream(withExitGrace(tctx), id, wrapWithPidFile(pidFile, cmd), env)
	if err != nil {
		cancel()
		finishOperation()
		r.markExecutionUnknown(folder, id)
		// The marker is committed and exec creation may have reached the daemon:
		// no exit evidence, so the ledger holds the attempt as unknown.
		return nil, fmt.Errorf("runner: task exec stream: %w", err)
	}
	started, gateHeld = true, false

	chunks := make(chan string, 32)
	result := make(chan RunResult, 1)
	finished := make(chan struct{})
	killed := make(chan bool, 1)
	exitWait := r.managed.exitWait
	if exitWait <= 0 {
		exitWait = defaultTaskExitWait
	}

	go func() { // watcher: kill on timeout/cancel, keep the idle watcher away
		tick := time.NewTicker(taskTouchInterval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				r.touch(folder)
			case <-tctx.Done():
				r.killAgent(tctx, id, folder, pidFile)
				select { // give the daemon a chance to report the exit
				case <-finished:
				case <-time.After(exitWait):
				}
				handle.Close()
				killed <- true
				return
			case <-finished:
				killed <- false
				return
			}
		}
	}()

	go func() {
		defer close(chunks)
		defer close(result)
		defer cancel()
		defer finishOperation()
		defer handle.Close()
		defer releaseSlot()

		r.touch(folder)
		emit := func(s string) {
			select {
			case chunks <- s:
			case <-tctx.Done():
			}
		}
		out, errOut := readExecStream(handle.Stdout, handle.Stderr, emit)
		stderr := strings.TrimSpace(errOut)

		var execErr error
		select {
		case execErr = <-handle.Done:
		case <-tctx.Done():
			// Timed out or cancelled: the watcher kills the agent. Wait for the
			// daemon's report, but not forever.
			select {
			case execErr = <-handle.Done:
			case <-time.After(exitWait + time.Second):
				execErr = errors.New("runner: task exec did not report completion")
			}
		}
		close(finished)
		wasKilled := <-killed
		if !wasKilled {
			r.removePidFile(tctx, id, pidFile)
		}
		if _, observed := handle.ExitObserved(); !observed && tctx.Err() == nil {
			r.markExecutionUnknown(folder, id)
		}
		r.releaseGateWhenSettled(ctx, folder, releaseGate)
		if code, ok := handle.ExitObserved(); ok {
			ev := fmt.Sprintf("exec_exit_observed code=%d", code)
			if wasKilled {
				ev += " after_kill"
			}
			req.Hooks.Exited(ev)
		}
		r.touch(folder)

		res := RunResult{FullText: strings.TrimSpace(out), Stderr: stderr}
		switch {
		case ctx.Err() != nil:
			res.Err = fmt.Errorf("runner: task cancelled: %w", ctx.Err())
		case tctx.Err() != nil:
			res.Err = fmt.Errorf("runner: task exceeded %s: %w", timeout, context.DeadlineExceeded)
		case execErr != nil:
			res.Err = r.classifyTaskExit(prov, stderr, out, execErr)
		}
		result <- res
	}()

	return &AgentStream{Chunks: chunks, Result: result}, nil
}

// classifyTaskExit maps a nonzero exit to a typed error without running any
// further exec in the container.
func (r *Runner) classifyTaskExit(prov AgentProvider, stderr, stdout string, err error) error {
	var ee *ExecError
	if errors.As(err, &ee) && ee.Code != 0 {
		switch {
		case isUsageLimit(stderr, stdout):
			return &UsageLimitError{Provider: prov.Name()}
		case prov.IsNotLoggedInError(stderr, stdout):
			return ErrNotLoggedIn
		}
		return fmt.Errorf("runner: task agent exited %d (stderr=%q)", ee.Code, tailString(stderr, 300))
	}
	return fmt.Errorf("runner: task exec: %w", err)
}
