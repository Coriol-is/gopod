package store

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

var allExecStates = []ExecState{ExecQueued, ExecReserved, ExecRunning, ExecCancelRequested, ExecCompleted, ExecFailed, ExecCancelled, ExecInterrupted, ExecExecutionUnknown}

func TestExecutionTransitionGraph(t *testing.T) {
	for _, from := range allExecStates {
		for _, to := range allExecStates {
			if from.terminal() && (CanRunTransition(from, to) || CanAttemptTransition(from, to)) {
				t.Errorf("terminal %s may move to %s", from, to)
			}
			if to == ExecReserved && CanRunTransition(from, to) {
				t.Errorf("run may enter the attempt-only state reserved from %s", from)
			}
		}
	}
	for _, edge := range [][2]ExecState{{ExecQueued, ExecCompleted}, {ExecRunning, ExecCancelled}, {ExecCancelRequested, ExecCompleted}, {ExecQueued, ExecInterrupted}} {
		if CanRunTransition(edge[0], edge[1]) {
			t.Errorf("run edge %s -> %s must not exist (a cancel race cannot fabricate completion)", edge[0], edge[1])
		}
	}
	if !CanAttemptTransition(ExecQueued, ExecReserved) || CanAttemptTransition(ExecQueued, ExecRunning) {
		t.Error("an attempt must be reserved before it runs")
	}
}

func admitted(t *testing.T, f *deliveryFixture) (ManagedTask, Admission) {
	t.Helper()
	task := f.create(f.agentSpec())
	return task, f.mustAdmit(admit(task, baseTime, baseTime+1000))
}

func TestTypedRunAndAttemptTransitions(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	_, a := admitted(t, f)

	wantErr(t, f.st.TransitionRun(ctx, a.RunID, ExecQueued, ExecCompleted, TransitionOptions{}), ErrInvalidTransition)
	wantErr(t, f.st.TransitionAttempt(ctx, a.AttemptID, ExecQueued, ExecReserved, TransitionOptions{}), ErrInvalidTransition) // no provider snapshot
	if err := f.st.TransitionAttempt(ctx, a.AttemptID, ExecQueued, ExecReserved, TransitionOptions{Provider: "claude", Model: "m", ProfileFingerprint: "fp"}); err != nil {
		t.Fatal(err)
	}
	att, _ := f.st.GetTaskAttempt(ctx, a.AttemptID)
	if att.State != ExecReserved || att.Reservation != "held" || att.Provider != "claude" {
		t.Fatalf("reserved: %+v", att)
	}
	wantErr(t, f.st.TransitionAttempt(ctx, a.AttemptID, ExecQueued, ExecCancelled, TransitionOptions{}), ErrTransitionConflict)
	wantErr(t, f.st.TransitionAttempt(ctx, "nope", ExecQueued, ExecCancelled, TransitionOptions{}), ErrAttemptNotFound)
	if err := f.st.TransitionAttempt(ctx, a.AttemptID, ExecReserved, ExecRunning, TransitionOptions{NativeSessionUUID: "native-1"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.TransitionRun(ctx, a.RunID, ExecQueued, ExecRunning, TransitionOptions{}); err != nil {
		t.Fatal(err)
	}
	// A started attempt cannot settle without host evidence, and keeps its snapshot.
	wantErr(t, f.st.TransitionAttempt(ctx, a.AttemptID, ExecRunning, ExecCompleted, TransitionOptions{}), ErrInvalidTransition)
	wantErr(t, f.st.TransitionRun(ctx, a.RunID, ExecRunning, ExecCompleted, TransitionOptions{}), ErrInvalidTransition)
	if execErr(f.st, `UPDATE task_attempts SET provider='codex' WHERE attempt_id=?`, a.AttemptID) == nil {
		t.Fatal("provider snapshot was rewritten")
	}
	if err := f.st.TransitionAttempt(ctx, a.AttemptID, ExecRunning, ExecCompleted, TransitionOptions{Evidence: "docker_exec_exit:0"}); err != nil {
		t.Fatal(err)
	}
	att, _ = f.st.GetTaskAttempt(ctx, a.AttemptID)
	if att.Reservation != "released" || att.ExitEvidence != "docker_exec_exit:0" || att.NativeSessionUUID != "native-1" || att.StartedAt == 0 || att.ExitedAt == 0 {
		t.Fatalf("settled attempt: %+v", att)
	}
	if err := f.st.TransitionRun(ctx, a.RunID, ExecRunning, ExecCompleted, TransitionOptions{Evidence: "exit_observed"}); err != nil {
		t.Fatal(err)
	}
	run, _ := f.st.GetTaskRunRecord(ctx, a.RunID)
	// Completion is execution only: goal stays unknown, nothing verified or delivered.
	if run.State != ExecCompleted || run.SettledAt == 0 || run.GoalStatus != "unknown" || run.GoalAuthority != "none" || run.PublicationState != "not_requested" || run.EffectsState != "none" {
		t.Fatalf("run: %+v", run)
	}
	// Terminal states are final, even for raw SQL.
	if execErr(f.st, `UPDATE task_runs SET execution_state='running' WHERE run_id=?`, a.RunID) == nil {
		t.Fatal("terminal run reopened")
	}
	if execErr(f.st, `UPDATE task_attempts SET execution_state='running' WHERE attempt_id=?`, a.AttemptID) == nil {
		t.Fatal("terminal attempt reopened")
	}
}

func TestConcurrentTransitionsExactlyOneWins(t *testing.T) {
	f := newDeliveryFixture(t)
	_, a := admitted(t, f)
	var wg sync.WaitGroup
	errs := make([]error, 6)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = f.st.TransitionRun(context.Background(), a.RunID, ExecQueued, ExecRunning, TransitionOptions{})
		}()
	}
	wg.Wait()
	wins := 0
	for _, e := range errs {
		if e == nil {
			wins++
		} else {
			wantErr(t, e, ErrTransitionConflict)
		}
	}
	if wins != 1 {
		t.Fatalf("%d transitions won", wins)
	}
}

func TestUnknownExecutionKeepsReservationAndNeedsEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	_, a := admitted(t, f)
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(f.st.TransitionAttempt(ctx, a.AttemptID, ExecQueued, ExecReserved, TransitionOptions{Provider: "claude"}))
	must(f.st.TransitionAttempt(ctx, a.AttemptID, ExecReserved, ExecRunning, TransitionOptions{}))
	must(f.st.TransitionRun(ctx, a.RunID, ExecQueued, ExecRunning, TransitionOptions{}))
	// Crash recovery: may have started, so unknown with effects unresolved.
	must(f.st.TransitionAttempt(ctx, a.AttemptID, ExecRunning, ExecExecutionUnknown, TransitionOptions{}))
	must(f.st.TransitionRun(ctx, a.RunID, ExecRunning, ExecExecutionUnknown, TransitionOptions{}))
	run, _ := f.st.GetTaskRunRecord(ctx, a.RunID)
	att, _ := f.st.GetTaskAttempt(ctx, a.AttemptID)
	if run.EffectsState != "unresolved" || att.Reservation != "held" {
		t.Fatalf("unknown: run=%+v att=%+v", run, att)
	}
	// Settling needs operator evidence; no evidence, no settle.
	wantErr(t, f.st.TransitionRun(ctx, a.RunID, ExecExecutionUnknown, ExecCancelled, TransitionOptions{}), ErrInvalidTransition)
	// The reservation is released only with proof and a bounded detail.
	wantErr(t, f.st.ReleaseAttemptReservation(ctx, a.AttemptID, ProofLocalExit, ""), ErrReservationNotReleasable)
	wantErr(t, f.st.ReleaseAttemptReservation(ctx, a.AttemptID, "guess", "x"), ErrReservationNotReleasable)
	must(f.st.ReleaseAttemptReservation(ctx, a.AttemptID, ProofLocalExit, "container_gone"))
	wantErr(t, f.st.ReleaseAttemptReservation(ctx, a.AttemptID, ProofLocalExit, "again"), ErrReservationNotReleasable)
	att, _ = f.st.GetTaskAttempt(ctx, a.AttemptID)
	if att.Reservation != "released" || !strings.HasPrefix(att.ExitEvidence, "local_exit_observed:") || att.State != ExecExecutionUnknown {
		t.Fatalf("released: %+v", att)
	}
	// Local exit does not settle effects: they stay unresolved and still forbid purge.
	run, _ = f.st.GetTaskRunRecord(ctx, a.RunID)
	if run.State != ExecExecutionUnknown || run.EffectsState != "unresolved" {
		t.Fatalf("run after release: %+v", run)
	}
	if execErr(f.st, `UPDATE task_attempts SET reservation_state='held' WHERE attempt_id=?`, a.AttemptID) == nil {
		t.Fatal("released reservation re-held")
	}
	must(f.st.TransitionRun(ctx, a.RunID, ExecExecutionUnknown, ExecFailed, TransitionOptions{Evidence: "operator_checked", FailureCode: "unknown_resolved"}))
	must(f.st.ReconcileRunEffects(ctx, a.RunID, "operator verified no remote effects"))
	wantErr(t, f.st.ReconcileRunEffects(ctx, a.RunID, "again"), ErrTransitionConflict)
	// A running attempt is never releasable.
	_, b := admittedIn(t, f)
	must(f.st.TransitionAttempt(ctx, b.AttemptID, ExecQueued, ExecReserved, TransitionOptions{Provider: "claude"}))
	must(f.st.TransitionAttempt(ctx, b.AttemptID, ExecReserved, ExecRunning, TransitionOptions{}))
	wantErr(t, f.st.ReleaseAttemptReservation(ctx, b.AttemptID, ProofOperatorReconciled, "trust me"), ErrReservationNotReleasable)
}

func admittedIn(t *testing.T, f *deliveryFixture) (ManagedTask, Admission) {
	t.Helper()
	return admitted(t, f)
}

func TestStopRunIsSeparateFromScheduleCancel(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task, a := admitted(t, f)

	// Pausing the schedule (cancel of future work) leaves the admitted run alone.
	paused, err := f.st.SetManagedTaskStatus(ctx, "owner", task.ID, 1, "paused", 0)
	if err != nil {
		t.Fatal(err)
	}
	if run, _ := f.st.GetTaskRunRecord(ctx, a.RunID); run.State != ExecQueued || run.StopRequestedAt != 0 {
		t.Fatalf("pause touched the run: %+v", run)
	}
	// Scope/run binding: wrong folder, wrong task, bad reason.
	_, err = f.st.RequestRunStop(ctx, "elsewhere", task.ID, a.RunID, "owner_stop")
	wantErr(t, err, ErrRunNotFound)
	_, err = f.st.RequestRunStop(ctx, "owner", "other-task", a.RunID, "owner_stop")
	wantErr(t, err, ErrRunNotFound)
	_, err = f.st.RequestRunStop(ctx, "owner", task.ID, a.RunID, "Because I said so")
	wantErr(t, err, ErrInvalidTransition)

	// Proven never started: cancelled at once, turn stopped, schedule untouched.
	res, err := f.st.RequestRunStop(ctx, "owner", task.ID, a.RunID, "owner_stop")
	if err != nil || !res.Settled || res.State != ExecCancelled {
		t.Fatalf("stop unstarted: %+v %v", res, err)
	}
	turn, _ := f.st.GetTurn(ctx, a.TurnID)
	if turn.Status != TurnStopped {
		t.Fatalf("turn: %+v", turn)
	}
	if after, _ := f.st.GetManagedTask(ctx, task.ID); after.Status != paused.Status || after.Revision != paused.Revision || after.NextRun != paused.NextRun {
		t.Fatalf("stop-run changed the schedule: %+v", after)
	}
	_, err = f.st.RequestRunStop(ctx, "owner", task.ID, a.RunID, "owner_stop")
	wantErr(t, err, ErrRunSettled)

	// A started turn: only requested, never settled, reservation untouched.
	resumed, _ := f.st.SetManagedTaskStatus(ctx, "owner", task.ID, paused.Revision, "active", baseTime+1000)
	b := f.mustAdmit(admit(resumed, baseTime+1000, baseTime+2000))
	mustExec(t, f.st, `UPDATE turns SET status='running', attempts=1 WHERE id=?`, b.TurnID)
	res, err = f.st.RequestRunStop(ctx, "owner", task.ID, b.RunID, "owner_stop")
	if err != nil || res.Settled || res.State != ExecCancelRequested {
		t.Fatalf("stop started: %+v %v", res, err)
	}
	run, _ := f.st.GetTaskRunRecord(ctx, b.RunID)
	att, _ := f.st.GetTaskAttempt(ctx, b.AttemptID)
	if run.State != ExecCancelRequested || run.StopRequestedAt == 0 || run.SettledAt != 0 || att.State != ExecCancelRequested {
		t.Fatalf("requested: run=%+v att=%+v", run, att)
	}
	if turn, _ = f.st.GetTurn(ctx, b.TurnID); turn.Status != TurnRunning {
		t.Fatalf("started turn was rewritten: %+v", turn)
	}
	// Idempotent; a cancel race cannot become completed.
	if res, err = f.st.RequestRunStop(ctx, "owner", task.ID, b.RunID, "owner_stop"); err != nil || res.Settled {
		t.Fatalf("repeat: %+v %v", res, err)
	}
	wantErr(t, f.st.TransitionRun(ctx, b.RunID, ExecCancelRequested, ExecCompleted, TransitionOptions{Evidence: "x"}), ErrInvalidTransition)
}

func TestTombstoneCancelsUnstartedAndKeepsStartedEvidence(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	spec := f.agentSpec()
	spec.Overlap, spec.QueueLimit = OverlapQueue, 5
	task := f.create(spec)
	slot := baseTime
	cur := task
	var adms []Admission
	for i := 0; i < 4; i++ {
		adms = append(adms, f.mustAdmit(admit(cur, slot, slot+1000)))
		slot += 1000
		cur, _ = f.st.GetManagedTask(ctx, task.ID)
	}
	queued, started, unknown, frozen := adms[0], adms[1], adms[2], adms[3]
	// started: its durable turn is running; unknown: execution_unknown with a held reservation; frozen: result exists.
	mustExec(t, f.st, `UPDATE turns SET status='running', attempts=1 WHERE id IN (?,?,?)`, started.TurnID, unknown.TurnID, frozen.TurnID)
	if err := f.st.TransitionAttempt(ctx, unknown.AttemptID, ExecQueued, ExecReserved, TransitionOptions{Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.st, `UPDATE task_attempts SET execution_state='execution_unknown' WHERE attempt_id=?`, unknown.AttemptID)
	mustExec(t, f.st, `UPDATE task_runs SET execution_state='execution_unknown', effects_state='unresolved' WHERE run_id=?`, unknown.RunID)
	res := f.freeze(ExecIsolatedTask, "final text", ResultMember{TurnID: frozen.TurnID, Attempt: 1})
	mustExec(t, f.st, `UPDATE task_attempts SET execution_state='running', provider='claude' WHERE attempt_id=?`, frozen.AttemptID)

	if err := f.st.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	state := func(a Admission) (ExecState, ExecState) {
		r, _ := f.st.GetTaskRunRecord(ctx, a.RunID)
		at, _ := f.st.GetTaskAttempt(ctx, a.AttemptID)
		return r.State, at.State
	}
	if r, at := state(queued); r != ExecCancelled || at != ExecCancelled {
		t.Fatalf("unstarted: %s/%s", r, at)
	}
	if turn, _ := f.st.GetTurn(ctx, queued.TurnID); turn.Status != TurnStopped {
		t.Fatalf("unstarted turn: %+v", turn)
	}
	if r, at := state(started); r != ExecCancelRequested || at != ExecCancelRequested {
		t.Fatalf("started: %s/%s", r, at)
	}
	if r, at := state(unknown); r != ExecExecutionUnknown || at != ExecExecutionUnknown {
		t.Fatalf("unknown: %s/%s", r, at)
	}
	if u, _ := f.st.GetTaskRunRecord(ctx, unknown.RunID); u.StopRequestedAt == 0 {
		t.Fatalf("stop not recorded on the unknown run: %+v", u)
	}
	if att, _ := f.st.GetTaskAttempt(ctx, unknown.AttemptID); att.Reservation != "held" {
		t.Fatalf("reservation released by deletion: %+v", att)
	}
	if r, _ := state(frozen); r != ExecCancelRequested {
		t.Fatalf("frozen: %s", r)
	}
	if got, err := f.st.GetTurnResult(ctx, res.ID); err != nil || got.Text != "final text" {
		t.Fatalf("frozen result lost: %+v %v", got, err)
	}
	// Everything is still there, the definition is only a tombstone, and the evidence blocks purge.
	if n := countRows(t, f.st, `SELECT count(*) FROM task_runs`); n != 4 {
		t.Fatalf("runs: %d", n)
	}
	blockers, err := f.st.TaskPurgeBlockers(ctx, task.ID)
	if err != nil || slices.Contains(blockers, BlockDefinitionActive) {
		t.Fatalf("blockers: %v %v", blockers, err)
	}
	for _, want := range []PurgeBlocker{BlockRunUnsettled, BlockReservationHeld, BlockEffectsUnresolved, BlockAuditExportUnchecked} {
		if !slices.Contains(blockers, want) {
			t.Errorf("missing blocker %s in %v", want, blockers)
		}
	}
	wantErr(t, f.st.RequireTaskPurgeable(ctx, task.ID), ErrPurgeBlocked)
}

func TestTombstoneCancelsPendingReminderButNeverUnsendsUncertain(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	spec := f.reminderSpec()
	spec.ScheduleType, spec.ScheduleValue = "interval", "1h"
	task := f.create(spec)
	first := f.mustAdmit(admit(task, baseTime, baseTime+1000))
	cur, _ := f.st.GetManagedTask(ctx, task.ID)
	second := f.mustAdmit(admit(cur, baseTime+1000, baseTime+2000))
	// The first send is in doubt: uncertain, unresolved.
	mustExec(t, f.st, `UPDATE outbox_parts SET state='uncertain' WHERE delivery_id=?`, first.DeliveryID)
	mustExec(t, f.st, `UPDATE delivery_groups SET state='uncertain', unresolved=1 WHERE delivery_id=?`, first.DeliveryID)

	if err := f.st.DeleteTask(ctx, task.ID); err != nil {
		t.Fatal(err)
	}
	d1, _ := f.st.GetDelivery(ctx, first.DeliveryID)
	d2, _ := f.st.GetDelivery(ctx, second.DeliveryID)
	if d1.State != DeliveryUncertain || !d1.Unresolved {
		t.Fatalf("uncertain send relabelled: %+v", d1)
	}
	if d2.State != DeliveryCancelled || d2.Unresolved {
		t.Fatalf("pending reminder not cancelled: %+v", d2)
	}
	blockers, _ := f.st.TaskPurgeBlockers(ctx, task.ID)
	if !slices.Contains(blockers, BlockDeliveryUnresolved) {
		t.Fatalf("blockers: %v", blockers)
	}
}

func TestPurgeBlockersAndRawEvidenceDeletionRejected(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task, a := admitted(t, f)
	b, _ := f.st.TaskPurgeBlockers(ctx, task.ID)
	if !slices.Contains(b, BlockDefinitionActive) || !slices.Contains(b, BlockRunUnsettled) {
		t.Fatalf("active definition: %v", b)
	}
	mustExec(t, f.st, `UPDATE task_runs SET publication_state='pending' WHERE run_id=?`, a.RunID)
	if b, _ = f.st.TaskPurgeBlockers(ctx, task.ID); !slices.Contains(b, BlockPublicationPending) {
		t.Fatalf("publication: %v", b)
	}
	// Even a fully settled, tombstoned task is not purgeable without a verified audit export.
	mustExec(t, f.st, `UPDATE task_runs SET publication_state='published', execution_state='failed', settled_at=1 WHERE run_id=?`, a.RunID)
	mustExec(t, f.st, `UPDATE task_attempts SET execution_state='failed' WHERE attempt_id=?`, a.AttemptID)
	if err := f.st.DeleteManagedTask(ctx, "owner", task.ID, 1, "deleted_by_owner"); err != nil {
		t.Fatal(err)
	}
	if b, _ = f.st.TaskPurgeBlockers(ctx, task.ID); len(b) != 1 || b[0] != BlockAuditExportUnchecked {
		t.Fatalf("settled blockers: %v", b)
	}
	// Raw deletion of any managed ledger row, or of a referenced turn, is rejected.
	rec, _ := f.st.RecordTaskVerification(ctx, TaskVerification{RunID: a.RunID, CheckerName: "hash", CheckerVersion: "1", CheckedDigest: strings.Repeat("a", 64), Outcome: VerifyInconclusive})
	for _, q := range []string{
		`DELETE FROM scheduled_tasks WHERE id='` + task.ID + `'`,
		`DELETE FROM task_runs`,
		`DELETE FROM task_attempts`,
		`DELETE FROM task_occurrences`,
		`DELETE FROM task_verifications`,
		`DELETE FROM turns WHERE id=` + strconv.FormatInt(a.TurnID, 10),
		`UPDATE task_verifications SET check_outcome='passed' WHERE receipt_id='` + rec.ReceiptID + `'`,
		`UPDATE task_occurrences SET reason='x'`,
	} {
		if execErr(f.st, q) == nil {
			t.Errorf("accepted: %s", q)
		}
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM task_runs`) + countRows(t, f.st, `SELECT count(*) FROM turns`); n != 2 {
		t.Fatalf("evidence lost: %d", n)
	}
}

func TestVerificationReceiptsAreHostOnlyAndScopedToTheRun(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	_, a := admitted(t, f)
	_, other := admitted(t, f)
	good := TaskVerification{RunID: a.RunID, AttemptID: a.AttemptID, CheckerName: "artifact-hash", CheckerVersion: "1.2", CheckedDigest: strings.Repeat("0", 64), Outcome: VerifyPassed}
	if v, err := f.st.RecordTaskVerification(ctx, good); err != nil || v.ReceiptID == "" || v.CheckedAt == 0 {
		t.Fatalf("receipt: %+v %v", v, err)
	}
	for name, mutate := range map[string]func(*TaskVerification){
		"no checker":       func(v *TaskVerification) { v.CheckerName = "" },
		"no version":       func(v *TaskVerification) { v.CheckerVersion = "" },
		"bad digest":       func(v *TaskVerification) { v.CheckedDigest = "xyz" },
		"bad outcome":      func(v *TaskVerification) { v.Outcome = "achieved" },
		"foreign attempt":  func(v *TaskVerification) { v.AttemptID = other.AttemptID },
		"unknown run":      func(v *TaskVerification) { v.RunID = "nope" },
		"unrelated result": func(v *TaskVerification) { v.ResultID = "no-such-result" },
	} {
		v := good
		mutate(&v)
		if _, err := f.st.RecordTaskVerification(ctx, v); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM task_verifications`); n != 1 {
		t.Fatalf("receipts: %d", n)
	}
}
