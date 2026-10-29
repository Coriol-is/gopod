package store

import (
	"context"
	"sync"
	"testing"
)

var testSnap = DispatchSnapshot{Provider: "claude", Model: "m", ProfileFingerprint: "fp"}

func TestTurnCarriesRunAttemptAndSessionKind(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	_, a := admitted(t, f)
	turn, err := f.st.GetTurn(ctx, a.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.RunID != a.RunID || turn.AttemptID != a.AttemptID || turn.SessionKind != "isolated" {
		t.Fatalf("managed turn links: %+v", turn)
	}
	unfinished, _ := f.st.ListUnfinishedTurns(ctx)
	if len(unfinished) != 1 || unfinished[0].AttemptID != a.AttemptID {
		t.Fatalf("unfinished: %+v", unfinished)
	}
	// An interactive turn has no links.
	plain, _ := f.st.GetTurn(ctx, f.turn("telegram", "1:1", 0, 0))
	if plain.RunID != "" || plain.AttemptID != "" || plain.SessionKind != "" {
		t.Fatalf("interactive turn got links: %+v", plain)
	}
}

func TestReserveTaskAttemptPersistsSnapshotBeforeSpawn(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	_, a := admitted(t, f)
	if _, err := f.st.ReserveTaskAttempt(ctx, a.AttemptID, DispatchSnapshot{}, baseTime); err == nil {
		t.Fatal("reserved without a provider snapshot")
	}
	res, err := f.st.ReserveTaskAttempt(ctx, a.AttemptID, testSnap, baseTime)
	if err != nil || res.Kind != ReserveNew {
		t.Fatalf("reserve: %+v %v", res, err)
	}
	att, _ := f.st.GetTaskAttempt(ctx, a.AttemptID)
	if att.State != ExecReserved || att.Reservation != "held" || att.Provider != "claude" || att.Model != "m" || att.ProfileFingerprint != "fp" || att.StartedAt != 0 {
		t.Fatalf("attempt after reserve: %+v", att)
	}
	// Second call is a resume of the same reservation and keeps the first snapshot.
	res, err = f.st.ReserveTaskAttempt(ctx, a.AttemptID, DispatchSnapshot{Provider: "codex", Model: "x"}, baseTime)
	if err != nil || res.Kind != ReserveResume || res.Attempt.Provider != "claude" {
		t.Fatalf("resume: %+v %v", res, err)
	}
}

func TestReserveRechecksScopeExpiryTombstoneAndStop(t *testing.T) {
	t.Run("renamed chat fails closed and cancels the unstarted run", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, a := admitted(t, f)
		mustExec(t, f.st, `UPDATE registered_chats SET registration_revision=registration_revision+1`)
		res, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime)
		if err != nil || res.Kind != ReserveRejected || res.Reason != "scope_changed" {
			t.Fatalf("reserve: %+v %v", res, err)
		}
		run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
		att, _ := f.st.GetTaskAttempt(t.Context(), a.AttemptID)
		turn, _ := f.st.GetTurn(t.Context(), a.TurnID)
		if run.State != ExecCancelled || att.State != ExecCancelled || att.Reservation != "none" || turn.Status != TurnStopped {
			t.Fatalf("run=%s attempt=%s/%s turn=%s", run.State, att.State, att.Reservation, turn.Status)
		}
	})
	t.Run("expired definition", func(t *testing.T) {
		f := newDeliveryFixture(t)
		spec := f.agentSpec()
		spec.ExpiresAt = baseTime + 5000
		task := f.create(spec)
		a := f.mustAdmit(admit(task, baseTime, baseTime+1000))
		res, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime+6000)
		if err != nil || res.Kind != ReserveRejected || res.Reason != "expired" {
			t.Fatalf("reserve: %+v %v", res, err)
		}
	})
	t.Run("stop requested before dispatch", func(t *testing.T) {
		f := newDeliveryFixture(t)
		task, a := admitted(t, f)
		if _, err := f.st.RequestRunStop(t.Context(), "owner", task.ID, a.RunID, "operator_stop"); err != nil {
			t.Fatal(err)
		}
		res, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime)
		if err != nil || res.Kind != ReserveRejected {
			t.Fatalf("reserve after stop: %+v %v", res, err)
		}
	})
	t.Run("frozen result is never redispatched", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, a := admitted(t, f)
		f.st.MarkTurnRunning(t.Context(), a.TurnID)
		f.freeze(ExecIsolatedTask, "done", ResultMember{TurnID: a.TurnID, Attempt: 1})
		res, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime)
		if err != nil || res.Kind != ReserveRejected || res.Reason != "result_frozen" {
			t.Fatalf("reserve: %+v %v", res, err)
		}
	})
}

func TestSpawnMarkerIsTheProofOfPossibleStart(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task, a := admitted(t, f)
	wantErr(t, f.st.BeginTaskSpawn(ctx, a.AttemptID), ErrTransitionConflict) // not reserved yet
	if _, err := f.st.ReserveTaskAttempt(ctx, a.AttemptID, testSnap, baseTime); err != nil {
		t.Fatal(err)
	}
	// A stop landing between reservation and spawn blocks the spawn.
	if _, err := f.st.RequestRunStop(ctx, "owner", task.ID, a.RunID, "operator_stop"); err != nil {
		t.Fatal(err)
	}
	wantErr(t, f.st.BeginTaskSpawn(ctx, a.AttemptID), ErrTransitionConflict)

	f2 := newDeliveryFixture(t)
	_, b := admitted(t, f2)
	if _, err := f2.st.ReserveTaskAttempt(ctx, b.AttemptID, testSnap, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := f2.st.BeginTaskSpawn(ctx, b.AttemptID); err != nil {
		t.Fatal(err)
	}
	run, _ := f2.st.GetTaskRunRecord(ctx, b.RunID)
	att, _ := f2.st.GetTaskAttempt(ctx, b.AttemptID)
	if run.State != ExecRunning || att.State != ExecRunning || att.StartedAt == 0 || att.Reservation != "held" {
		t.Fatalf("after marker: run=%s attempt=%+v", run.State, att)
	}
}

func startedAttempt(t *testing.T, f *deliveryFixture) Admission {
	t.Helper()
	_, a := admitted(t, f)
	if _, err := f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime); err != nil {
		t.Fatal(err)
	}
	if err := f.st.BeginTaskSpawn(t.Context(), a.AttemptID); err != nil {
		t.Fatal(err)
	}
	return a
}

func TestSettleTaskAttemptMapsOnlyObservedExitToTerminalStates(t *testing.T) {
	cases := []struct {
		name     string
		s        Settlement
		want     ExecState
		released bool
	}{
		{"exited", Settlement{Kind: SettleExited, Evidence: "container_exec_exit:0"}, ExecCompleted, true},
		{"failed", Settlement{Kind: SettleFailed, Evidence: "container_exec_exit:1"}, ExecFailed, true},
		{"stopped", Settlement{Kind: SettleStopped, Evidence: "container_exec_killed"}, ExecCancelled, true},
		{"interrupted", Settlement{Kind: SettleInterrupted, Evidence: "shutdown_exit_observed"}, ExecInterrupted, true},
		{"no exit proof", Settlement{Kind: SettleUnknown}, ExecExecutionUnknown, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newDeliveryFixture(t)
			a := startedAttempt(t, f)
			got, err := f.st.SettleTaskAttempt(t.Context(), a.AttemptID, c.s)
			if err != nil || got != c.want {
				t.Fatalf("settle = %s, %v; want %s", got, err, c.want)
			}
			att, _ := f.st.GetTaskAttempt(t.Context(), a.AttemptID)
			run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
			if att.State != c.want || run.State != c.want {
				t.Fatalf("attempt=%s run=%s", att.State, run.State)
			}
			if (att.Reservation == "released") != c.released {
				t.Fatalf("reservation = %s", att.Reservation)
			}
			if c.want == ExecExecutionUnknown && run.EffectsState != "unresolved" {
				t.Fatalf("unknown run effects = %s", run.EffectsState)
			}
			// Settling twice is idempotent and never reopens or changes it.
			again, err := f.st.SettleTaskAttempt(t.Context(), a.AttemptID, c.s)
			if c.want.terminal() && (err != nil || again != c.want) {
				t.Fatalf("second settle = %s, %v", again, err)
			}
		})
	}
	t.Run("exit kinds need evidence", func(t *testing.T) {
		f := newDeliveryFixture(t)
		a := startedAttempt(t, f)
		_, err := f.st.SettleTaskAttempt(t.Context(), a.AttemptID, Settlement{Kind: SettleExited})
		wantErr(t, err, ErrInvalidTransition)
	})
	t.Run("cancel race never fabricates completion", func(t *testing.T) {
		f := newDeliveryFixture(t)
		task, a := admitted(t, f)
		ctx := t.Context()
		f.st.ReserveTaskAttempt(ctx, a.AttemptID, testSnap, baseTime)
		f.st.BeginTaskSpawn(ctx, a.AttemptID)
		if _, err := f.st.RequestRunStop(ctx, "owner", task.ID, a.RunID, "operator_stop"); err != nil {
			t.Fatal(err)
		}
		got, err := f.st.SettleTaskAttempt(ctx, a.AttemptID, Settlement{Kind: SettleExited, Evidence: "container_exec_exit:0"})
		if err != nil || got != ExecCancelled {
			t.Fatalf("settle during stop = %s, %v", got, err)
		}
	})
	t.Run("not started failure releases the reservation", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, a := admitted(t, f)
		f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime)
		got, err := f.st.SettleTaskAttempt(t.Context(), a.AttemptID, Settlement{Kind: SettleNotStarted, Evidence: "not_started:pre_exec_failed"})
		if err != nil || got != ExecFailed {
			t.Fatalf("settle = %s, %v", got, err)
		}
		att, _ := f.st.GetTaskAttempt(t.Context(), a.AttemptID)
		if att.Reservation != "released" {
			t.Fatalf("reservation = %s", att.Reservation)
		}
		// A stop that landed on a reserved attempt settles cancelled when the
		// controller proves its own spawn marker was refused.
		h := newDeliveryFixture(t)
		task, c := admitted(t, h)
		h.st.ReserveTaskAttempt(t.Context(), c.AttemptID, testSnap, baseTime)
		h.st.RequestRunStop(t.Context(), "owner", task.ID, c.RunID, "operator_stop")
		wantErr(t, h.st.BeginTaskSpawn(t.Context(), c.AttemptID), ErrTransitionConflict)
		if got, err := h.st.SettleTaskAttempt(t.Context(), c.AttemptID, Settlement{Kind: SettleNotStarted, Evidence: "not_started:spawn_marker_refused"}); err != nil || got != ExecCancelled {
			t.Fatalf("settle after refused marker = %s, %v", got, err)
		}
		// A started attempt can never be settled as not started.
		g := newDeliveryFixture(t)
		b := startedAttempt(t, g)
		_, err = g.st.SettleTaskAttempt(t.Context(), b.AttemptID, Settlement{Kind: SettleNotStarted, Evidence: "x"})
		wantErr(t, err, ErrTransitionConflict)
	})
}

func TestRecoverTaskAttemptNeverReplaysPotentiallyStartedWork(t *testing.T) {
	t.Run("queued dispatches", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, a := admitted(t, f)
		plan, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || plan.Action != RecoverDispatch {
			t.Fatalf("plan: %+v %v", plan, err)
		}
	})
	t.Run("reserved before the spawn marker dispatches again", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, a := admitted(t, f)
		f.st.ReserveTaskAttempt(t.Context(), a.AttemptID, testSnap, baseTime)
		plan, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || plan.Action != RecoverDispatch || plan.Attempt.State != ExecReserved {
			t.Fatalf("plan: %+v %v", plan, err)
		}
	})
	t.Run("running after the spawn marker becomes execution_unknown and holds", func(t *testing.T) {
		f := newDeliveryFixture(t)
		a := startedAttempt(t, f)
		plan, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || plan.Action != RecoverHold || plan.Attempt.State != ExecExecutionUnknown || plan.Attempt.Reservation != "held" {
			t.Fatalf("plan: %+v %v", plan, err)
		}
		run, _ := f.st.GetTaskRunRecord(t.Context(), a.RunID)
		if run.State != ExecExecutionUnknown || run.EffectsState != "unresolved" {
			t.Fatalf("run: %+v", run)
		}
		held, err := f.st.HeldTaskReservations(t.Context())
		if err != nil || len(held) != 1 || held[0].AttemptID != a.AttemptID || held[0].Folder != "owner" {
			t.Fatalf("held: %+v %v", held, err)
		}
		// Idempotent across a second restart.
		again, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || again.Action != RecoverHold {
			t.Fatalf("second recover: %+v %v", again, err)
		}
		// Operator proof releases the hold; the run stays unknown, not replayable.
		if err := f.st.ReleaseAttemptReservation(t.Context(), a.AttemptID, ProofOperatorReconciled, "container removed"); err != nil {
			t.Fatal(err)
		}
		if held, _ = f.st.HeldTaskReservations(t.Context()); len(held) != 0 {
			t.Fatalf("held after release: %+v", held)
		}
		final, _ := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if final.Action == RecoverDispatch {
			t.Fatal("released unknown attempt became dispatchable")
		}
	})
	t.Run("frozen result is publication only", func(t *testing.T) {
		f := newDeliveryFixture(t)
		a := startedAttempt(t, f)
		f.st.MarkTurnRunning(t.Context(), a.TurnID)
		res := f.freeze(ExecIsolatedTask, "final", ResultMember{TurnID: a.TurnID, Attempt: 1})
		plan, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || plan.Action != RecoverFrozen || plan.ResultID != res.ID {
			t.Fatalf("plan: %+v %v (result %s)", plan, err, res.ID)
		}
		if plan.Attempt.State != ExecExecutionUnknown {
			t.Fatalf("frozen result must not prove exit: %s", plan.Attempt.State)
		}
	})
	t.Run("terminal ledger is settled", func(t *testing.T) {
		f := newDeliveryFixture(t)
		a := startedAttempt(t, f)
		f.st.SettleTaskAttempt(t.Context(), a.AttemptID, Settlement{Kind: SettleExited, Evidence: "exit:0"})
		plan, err := f.st.RecoverTaskAttempt(t.Context(), a.TurnID)
		if err != nil || plan.Action != RecoverSettled || plan.Attempt.State != ExecCompleted {
			t.Fatalf("plan: %+v %v", plan, err)
		}
	})
	t.Run("interactive turn is not managed", func(t *testing.T) {
		f := newDeliveryFixture(t)
		_, err := f.st.RecoverTaskAttempt(t.Context(), f.turn("telegram", "1:1", 0, 1))
		wantErr(t, err, ErrAttemptNotFound)
	})
}

func TestConcurrentReserveYieldsOneNewReservation(t *testing.T) {
	f := newDeliveryFixture(t)
	_, a := admitted(t, f)
	var wg sync.WaitGroup
	kinds := make([]ReserveKind, 5)
	for i := range kinds {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, err := f.st.ReserveTaskAttempt(context.Background(), a.AttemptID, testSnap, baseTime)
			if err != nil {
				t.Error(err)
			}
			kinds[i] = r.Kind
		}()
	}
	wg.Wait()
	n := 0
	for _, k := range kinds {
		if k == ReserveNew {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d new reservations: %v", n, kinds)
	}
}
