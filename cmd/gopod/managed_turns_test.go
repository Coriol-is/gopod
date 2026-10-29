package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

var _ queue.ManagedStore = managedLedger{}

const baseMs = int64(1_000_000_000_000)

type managedFixture struct {
	t    *testing.T
	st   *store.Store
	path string
	log  *slog.Logger
}

func newManagedFixture(t *testing.T) *managedFixture {
	t.Helper()
	f := &managedFixture{t: t, path: filepath.Join(t.TempDir(), "store.sqlite"), log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	f.reopen()
	if err := f.st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return f
}

// reopen simulates a process restart on the same database file.
func (f *managedFixture) reopen() {
	if f.st != nil {
		_ = f.st.Close()
	}
	st, err := store.Open(context.Background(), f.path, f.log)
	if err != nil {
		f.t.Fatal(err)
	}
	f.st = st
	f.t.Cleanup(func() { _ = st.Close() })
}

func (f *managedFixture) admit(nominal int64) store.Admission {
	f.t.Helper()
	ctx := f.t.Context()
	conv, err := f.st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
	if err != nil {
		f.t.Fatal(err)
	}
	task, err := f.st.CreateManagedTask(ctx, store.ManagedTask{
		Kind: store.TaskKindAgent, Conversation: conv, ActorRef: "owner:service", Prompt: "report",
		ScheduleType: "interval", ScheduleValue: "1h", Timezone: "UTC", NextRun: nominal,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	a, err := f.st.AdmitTaskOccurrence(ctx, store.AdmissionRequest{
		TaskID: task.ID, ExpectedRevision: task.Revision, ExpectedNextRun: task.NextRun,
		Now: nominal + 10, NominalAt: nominal, NextRun: nominal + 1000,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return a
}

// newQueue builds a queue like main does, optionally with a managed handler.
func (f *managedFixture) newQueue(handler queue.Handler) (*queue.Queue, *atomic.Int32) {
	var interactive atomic.Int32
	q := queue.New(context.Background(), func(context.Context, []queue.Item) error {
		interactive.Add(1)
		return nil
	}, 2, turnStoreAdapter{st: f.st}, f.log)
	q.BackoffFunc = func(int) time.Duration { return 0 }
	rt := queue.ManagedRuntime{Store: managedLedger{st: f.st}}
	if handler != nil {
		rt.Handler = handler
		rt.Snapshot = func(context.Context, queue.Item) (queue.Snapshot, error) {
			return queue.Snapshot{Provider: "claude", Model: "m", ProfileFingerprint: "fp"}, nil
		}
	}
	q.SetManaged(rt)
	f.t.Cleanup(q.Close)
	return q, &interactive
}

func waitState(t *testing.T, st *store.Store, attemptID string, want store.ExecState) store.TaskAttempt {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for {
		a, err := st.GetTaskAttempt(t.Context(), attemptID)
		if err == nil && a.State == want {
			return a
		}
		if time.Now().After(deadline) {
			t.Fatalf("attempt %s state = %v (err %v), want %s", attemptID, a.State, err, want)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func exitingHandler(native *atomic.Int32) queue.Handler {
	return func(ctx context.Context, items []queue.Item) error {
		e, _ := queue.ExecutionFrom(ctx)
		if err := e.BeginSpawn(ctx); err != nil {
			return err
		}
		native.Add(1)
		e.Exited("container_exec_exit:0")
		return nil
	}
}

func TestDispatchAfterCommitRunsThroughRealLedger(t *testing.T) {
	f := newManagedFixture(t)
	a := f.admit(baseMs)
	var native atomic.Int32
	q, _ := f.newQueue(exitingHandler(&native))
	if ok, err := q.Dispatch(t.Context(), a.TurnID); err != nil || !ok {
		t.Fatal(ok, err)
	}
	att := waitState(t, f.st, a.AttemptID, store.ExecCompleted)
	run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
	if att.Provider != "claude" || att.Reservation != "released" || att.ExitEvidence != "container_exec_exit:0" || run.State != store.ExecCompleted {
		t.Fatalf("attempt=%+v run=%+v", att, run)
	}
	if run.GoalStatus != "unknown" || run.PublicationState != "not_requested" {
		t.Fatalf("execution exit must not imply goal or publication: %+v", run)
	}
	if turn, _ := f.st.GetTurn(t.Context(), a.TurnID); turn.Status != store.TurnDone || turn.ReplyMsgID != 0 {
		t.Fatalf("turn: %+v", turn)
	}
	if native.Load() != 1 {
		t.Fatalf("native = %d", native.Load())
	}
}

func TestRestartRecoveryMatrixWithRealStore(t *testing.T) {
	t.Run("crash after commit before dispatch", func(t *testing.T) {
		f := newManagedFixture(t)
		a := f.admit(baseMs) // committed; process dies before q.Dispatch
		f.reopen()
		var native atomic.Int32
		q, _ := f.newQueue(exitingHandler(&native))
		if n, err := q.Recover(t.Context()); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		waitState(t, f.st, a.AttemptID, store.ExecCompleted)
		if native.Load() != 1 {
			t.Fatalf("native = %d", native.Load())
		}
	})
	t.Run("crash after reservation before spawn", func(t *testing.T) {
		f := newManagedFixture(t)
		a := f.admit(baseMs)
		if _, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, store.DispatchSnapshot{Provider: "codex", Model: "orig"}, baseMs); err != nil {
			t.Fatal(err)
		}
		f.reopen()
		var native atomic.Int32
		q, _ := f.newQueue(exitingHandler(&native))
		if n, err := q.Recover(t.Context()); err != nil || n != 1 {
			t.Fatal(n, err)
		}
		att := waitState(t, f.st, a.AttemptID, store.ExecCompleted)
		if att.Provider != "codex" || att.Model != "orig" {
			t.Fatalf("snapshot rewritten: %+v", att)
		}
		if native.Load() != 1 {
			t.Fatalf("native = %d", native.Load())
		}
	})
	t.Run("crash after spawn", func(t *testing.T) {
		f := newManagedFixture(t)
		a := f.admit(baseMs)
		f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, store.DispatchSnapshot{Provider: "claude"}, baseMs)
		f.st.MarkTurnRunning(t.Context(), a.TurnID)
		if err := f.st.BeginTaskSpawn(t.Context(), a.AttemptID); err != nil {
			t.Fatal(err)
		}
		for restart := 0; restart < 2; restart++ { // idempotent across repeated restarts
			f.reopen()
			var native atomic.Int32
			q, interactive := f.newQueue(exitingHandler(&native))
			if n, err := q.Recover(t.Context()); err != nil || n != restart { // only the earlier blocked chat turn replays
				t.Fatalf("restart %d: Recover = %d, %v", restart, n, err)
			}
			att, _ := f.st.GetTaskAttempt(t.Context(), a.AttemptID)
			run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
			if native.Load() != 0 || att.State != store.ExecExecutionUnknown || att.Reservation != "held" ||
				run.State != store.ExecExecutionUnknown || run.EffectsState != "unresolved" {
				t.Fatalf("restart %d: native=%d attempt=%+v run=%+v", restart, native.Load(), att, run)
			}
			if q.Holds() != 1 {
				t.Fatalf("holds = %d", q.Holds())
			}
			if turn, _ := f.st.GetTurn(t.Context(), a.TurnID); turn.Status != store.TurnFailed {
				t.Fatalf("turn status = %s", turn.Status)
			}
			// The held namespace takes no interactive work until released.
			q.Enqueue(t.Context(), queue.Item{Folder: "owner", Source: "telegram", SourceID: fmt.Sprintf("1:%d", restart+1), ChatID: 1, MessageID: restart + 1, Text: "hi"})
			time.Sleep(30 * time.Millisecond)
			if interactive.Load() != 0 {
				t.Fatal("interactive work ran beside an unknown task execution")
			}
			if restart == 1 {
				if err := q.ReleaseHold(t.Context(), a.AttemptID, queue.ProofOperatorReconciled, "container removed by operator"); err != nil {
					t.Fatal(err)
				}
				deadline := time.Now().Add(3 * time.Second)
				for interactive.Load() == 0 && time.Now().Before(deadline) {
					time.Sleep(5 * time.Millisecond)
				}
				if interactive.Load() < 1 || native.Load() != 0 {
					t.Fatalf("after release interactive=%d native=%d", interactive.Load(), native.Load())
				}
				if att, _ = f.st.GetTaskAttempt(t.Context(), a.AttemptID); att.Reservation != "released" || att.State != store.ExecExecutionUnknown {
					t.Fatalf("release must not settle or replay: %+v", att)
				}
			}
		}
	})
	t.Run("frozen result is publication only", func(t *testing.T) {
		f := newManagedFixture(t)
		a := f.admit(baseMs)
		ctx := t.Context()
		f.st.ReserveTaskAttempt(ctx, a.AttemptID, store.DispatchSnapshot{Provider: "claude"}, baseMs)
		f.st.MarkTurnRunning(ctx, a.TurnID)
		f.st.BeginTaskSpawn(ctx, a.AttemptID)
		conv, _ := f.st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
		res, err := f.st.FreezeTurnResult(ctx, store.ExecutionRef{Conversation: conv, Kind: store.ExecIsolatedTask,
			Members: []store.ResultMember{{TurnID: a.TurnID, Attempt: 1}}, Provider: "claude"}, "final text")
		if err != nil {
			t.Fatal(err)
		}
		f.reopen()
		var native, frozen atomic.Int32
		q, _ := f.newQueue(exitingHandler(&native))
		q.SetManaged(queue.ManagedRuntime{Store: managedLedger{st: f.st},
			OnFrozen: func(_ context.Context, _ queue.Item, id string) error {
				if id == res.ID {
					frozen.Add(1)
				}
				return nil
			}})
		if _, err := q.Recover(ctx); err != nil {
			t.Fatal(err)
		}
		if frozen.Load() != 1 || native.Load() != 0 {
			t.Fatalf("frozen=%d native=%d", frozen.Load(), native.Load())
		}
		if att, _ := f.st.GetTaskAttempt(ctx, a.AttemptID); att.State != store.ExecExecutionUnknown || att.Reservation != "held" {
			t.Fatalf("a frozen result proves no exit: %+v", att)
		}
	})
}

func TestInspectOnlyWiringLeavesManagedWorkPendingAndLegacyUntouched(t *testing.T) {
	f := newManagedFixture(t)
	a := f.admit(baseMs)
	legacyID, _, err := f.st.InsertTurn(t.Context(), store.Turn{Source: "telegram", SourceID: "1:1", ChatFolder: "owner", ChatID: 1, TGMessageID: 1, Text: "hello"})
	if err != nil {
		t.Fatal(err)
	}
	q, interactive := f.newQueue(nil) // production wiring in this PR: no managed handler
	n, err := q.Recover(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("Recover = %d, %v (only the legacy turn is scheduled)", n, err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for interactive.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if interactive.Load() != 1 {
		t.Fatal("legacy turn did not run")
	}
	if att, _ := f.st.GetTaskAttempt(t.Context(), a.AttemptID); att.State != store.ExecQueued {
		t.Fatalf("managed attempt changed without a runtime: %+v", att)
	}
	if turn, _ := f.st.GetTurn(t.Context(), a.TurnID); turn.Status != store.TurnPending {
		t.Fatalf("managed turn status = %s", turn.Status)
	}
	_ = legacyID
}
