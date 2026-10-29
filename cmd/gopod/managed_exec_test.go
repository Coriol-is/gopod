package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/taskexec"
	"github.com/Coriol-is/gopod/internal/taskreport"
)

type fakeTaskRunner struct {
	dataDir string
	snapErr error
	runErr  error
	reports func(env map[string]string) [][]byte // files the "agent" writes
	envSeen map[string]string
}

func (f *fakeTaskRunner) ResolveTaskSnapshot(context.Context, string) (runner.TaskSnapshot, error) {
	if f.snapErr != nil {
		return runner.TaskSnapshot{}, f.snapErr
	}
	return runner.TaskSnapshot{Provider: "claude", Model: "m", ProfileFingerprint: "epp1:x"}, nil
}

func (f *fakeTaskRunner) RunTaskStream(ctx context.Context, req runner.TaskRunRequest) (*runner.AgentStream, error) {
	if f.runErr != nil {
		return nil, f.runErr
	}
	if err := req.Hooks.BeginSpawn(ctx); err != nil {
		return nil, err
	}
	f.envSeen = map[string]string{}
	for _, kv := range req.Env {
		k, v, _ := strings.Cut(kv, "=")
		f.envSeen[k] = v
	}
	dir := filepath.Join(f.dataDir, "ipc", req.Folder, taskreport.DirName)
	for i, b := range f.reports(f.envSeen) {
		if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%02d.json", i)), b, 0o600); err != nil {
			return nil, err
		}
	}
	chunks, result := make(chan string, 1), make(chan runner.RunResult, 1)
	chunks <- "discarded"
	close(chunks)
	req.Hooks.Exited("exec_exit_observed code=0")
	result <- runner.RunResult{}
	close(result)
	return &runner.AgentStream{Chunks: chunks, Result: result}, nil
}

func report(env map[string]string, mut func(m map[string]any)) []byte {
	m := map[string]any{"version": 1, "runId": env["GOPOD_TASK_RUN_ID"], "attemptId": env["GOPOD_TASK_ATTEMPT_ID"],
		"runToken": env["GOPOD_TASK_REPORT_TOKEN"],
		"outcome":  map[string]any{"status": "achieved", "checks": []string{}, "evidence": []string{"/host/path"}, "limitations": []string{}}}
	if mut != nil {
		mut(m)
	}
	b, _ := json.Marshal(m)
	return b
}

func (f *managedFixture) executor(fr *fakeTaskRunner) (*taskexec.Executor, *queue.Queue) {
	fr.dataDir = f.t.TempDir()
	ex := &taskexec.Executor{Runner: fr, Reports: f.st, DataDir: fr.dataDir, Log: f.log,
		Grants: taskreport.NewGrants(nil, func(s store.ScopeRef) error { return f.st.ValidateScope(context.Background(), s) })}
	q := queue.New(context.Background(), func(context.Context, []queue.Item) error { return nil }, 2, turnStoreAdapter{st: f.st}, f.log)
	q.BackoffFunc = func(int) time.Duration { return 0 }
	q.SetManaged(queue.ManagedRuntime{Store: managedLedger{st: f.st}, Handler: ex.Handle, Snapshot: ex.Snapshot})
	f.t.Cleanup(q.Close)
	return ex, q
}

func TestExecutorRecordsProvisionalReportAndRejectsSpoofsAndRevokes(t *testing.T) {
	f := newManagedFixture(t)
	a := f.admit(baseMs)
	fr := &fakeTaskRunner{}
	fr.reports = func(env map[string]string) [][]byte {
		return [][]byte{
			report(env, nil),
			report(env, func(m map[string]any) { m["attemptId"] = "another-attempt" }), // spoof
			report(env, func(m map[string]any) { m["hostVerified"] = true }),           // forged authority
		}
	}
	ex, q := f.executor(fr)
	if err := os.MkdirAll(filepath.Join(fr.dataDir, "ipc", "owner", taskreport.DirName), 0o755); err != nil {
		t.Fatal(err)
	}
	if ok, err := q.Dispatch(t.Context(), a.TurnID); !ok || err != nil {
		t.Fatal(ok, err)
	}
	att := waitState(t, f.st, a.AttemptID, store.ExecCompleted)
	run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
	if att.ExitEvidence != "exec_exit_observed code=0" || att.Reservation != "released" {
		t.Fatalf("attempt = %+v", att)
	}
	if run.GoalStatus != "achieved" || run.GoalAuthority != "model_reported" || run.PublicationState != "not_requested" {
		t.Fatalf("report must be a model-reported claim only: %+v", run)
	}
	if ex.Grants.Active() != 0 {
		t.Fatal("capability outlived the attempt")
	}
}

func TestExecutorDenialSettlesNotStartedWithoutRetry(t *testing.T) {
	for name, fr := range map[string]*fakeTaskRunner{
		"snapshot": {snapErr: fmt.Errorf("%w: unqualified", runner.ErrTaskRoleUnqualified)},
		"run":      {runErr: fmt.Errorf("%w: policy changed", runner.ErrTaskDenied)},
	} {
		t.Run(name, func(t *testing.T) {
			f := newManagedFixture(t)
			a := f.admit(baseMs)
			_, q := f.executor(fr)
			if ok, err := q.Dispatch(t.Context(), a.TurnID); !ok || err != nil {
				t.Fatal(ok, err)
			}
			att := waitState(t, f.st, a.AttemptID, store.ExecFailed)
			if att.StartedAt != 0 || att.Reservation == "held" || q.Holds() != 0 {
				t.Fatalf("denied attempt looks started: %+v holds=%d", att, q.Holds())
			}
		})
	}
}
