package store

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAdmitAgentOccurrenceCreatesRunAttemptTurnAndAdvancesCursor(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	a := f.mustAdmit(admit(task, baseTime, baseTime+3_600_000))

	if a.RunID == "" || a.AttemptID == "" || a.TurnID == 0 || a.DeliveryID != "" {
		t.Fatalf("admission: %+v", a)
	}
	turn, err := f.st.GetTurn(ctx, a.TurnID)
	if err != nil {
		t.Fatal(err)
	}
	if turn.Source != "task" || turn.Status != TurnPending || turn.IsOwner || turn.ChatID != 0 || turn.Text != "run the report" ||
		turn.ThreadID != 0 || turn.ChatFolder != "owner" {
		t.Fatalf("turn: %+v", turn)
	}
	run, err := f.st.GetTaskRunRecord(ctx, a.RunID)
	if err != nil || run.State != ExecQueued || run.TaskID != task.ID || run.Revision != 1 || run.NominalAt != baseTime ||
		run.Conversation != f.conv || run.DeliveryMode != TaskDeliveryLegacyIPC || run.GoalStatus != "unknown" || run.GoalAuthority != "none" || run.PublicationState != "not_requested" {
		t.Fatalf("run: %+v %v", run, err)
	}
	att, err := f.st.GetTaskAttempt(ctx, a.AttemptID)
	if err != nil || att.TurnID != a.TurnID || att.State != ExecQueued || att.Number != 1 || att.Reservation != "none" {
		t.Fatalf("attempt: %+v %v", att, err)
	}
	got, _ := f.st.GetManagedTask(ctx, task.ID)
	if got.NextRun != baseTime+3_600_000 || got.LastRun != baseTime+10 || got.Revision != 1 || got.Status != "active" {
		t.Fatalf("cursor: %+v", got)
	}
	occ, _ := f.st.ListTaskOccurrences(ctx, task.ID, 10)
	if len(occ) != 1 || occ[0].Decision != DecisionAdmitted || occ[0].RunID != a.RunID || occ[0].Revision != 1 {
		t.Fatalf("occurrences: %+v", occ)
	}
	// The managed turn is compatible with the CP-002 isolated-task freeze and
	// never touches foreground history or the reply memo.
	mustExec(t, f.st, `UPDATE turns SET attempts=1, status='running' WHERE id=?`, a.TurnID)
	res := f.freeze(ExecIsolatedTask, "done", ResultMember{TurnID: a.TurnID, Attempt: 1})
	if res.Kind != ExecIsolatedTask || countRows(t, f.st, `SELECT count(*) FROM conversation_history`) != 0 {
		t.Fatalf("freeze: %+v", res)
	}
	if _, err = f.st.RecordTaskVerification(ctx, TaskVerification{RunID: a.RunID, AttemptID: a.AttemptID, ResultID: res.ID, CheckerName: "artifact-hash", CheckerVersion: "1", Outcome: VerifyPassed}); err != nil {
		t.Fatalf("verification tied to the run's result: %v", err)
	}
}

func TestAdmitReminderPublishesLiteralTextWithoutRunOrTurn(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.reminderSpec())
	req := admit(task, baseTime, 0)
	req.NewStatus, req.TerminalReason = "done", "completed_once"
	a := f.mustAdmit(req)
	if a.DeliveryID == "" || a.RunID != "" || a.TurnID != 0 {
		t.Fatalf("admission: %+v", a)
	}
	c := ledgerCounts(t, f.st)
	if c["turns"] != 0 || c["task_runs"] != 0 || c["task_attempts"] != 0 || c["delivery_groups"] != 1 || c["outbox_parts"] != 1 || c["task_occurrences"] != 1 {
		t.Fatalf("counts: %v", c)
	}
	d, err := f.st.GetDelivery(ctx, a.DeliveryID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Owner != OwnerControllerResult || d.TaskRef != task.ID || d.ResultID != "" || string(d.Parts[0].Part.Payload) != "stand up" ||
		d.ExpiresAt != baseTime+24*3_600_000 || d.State != DeliveryPending || d.ActorRef != "owner:service" {
		t.Fatalf("delivery: %+v", d)
	}
	got, _ := f.st.GetManagedTask(ctx, task.ID)
	if got.Status != "done" || got.NextRun != 0 || got.TerminalReason != "completed_once" {
		t.Fatalf("terminal state: %+v", got)
	}
}

func TestAdmitRejectsStaleDuplicateAndInvalidRequests(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	next := baseTime + 1000

	mut := func(m func(*AdmissionRequest)) AdmissionRequest { r := admit(task, baseTime, next); m(&r); return r }
	bad := []struct {
		name string
		req  AdmissionRequest
		want error
	}{
		{"stale revision", mut(func(r *AdmissionRequest) { r.ExpectedRevision = 7 }), ErrRevisionConflict},
		{"stale cursor", mut(func(r *AdmissionRequest) {
			r.ExpectedNextRun = baseTime - 1
			r.NominalAt = baseTime - 1
			r.NextRun = next
		}), ErrCursorConflict},
		{"cursor not moving forward", mut(func(r *AdmissionRequest) { r.NextRun = baseTime }), ErrCursorConflict},
		{"cursor rewound", mut(func(r *AdmissionRequest) { r.NextRun = baseTime - 5 }), ErrCursorConflict},
		{"future nominal", mut(func(r *AdmissionRequest) { r.NominalAt = r.Now + 1 }), ErrInvalidAdmission},
		{"no decision for a cursor move", mut(func(r *AdmissionRequest) { r.NominalAt = 0 }), ErrInvalidAdmission},
		{"unknown task", mut(func(r *AdmissionRequest) { r.TaskID = "nope" }), ErrTaskNotFound},
		{"decision repeats admitted slot", mut(func(r *AdmissionRequest) {
			r.Decisions = []SlotDecision{{NominalAt: baseTime, Decision: DecisionSkipped, Reason: "x"}}
		}), ErrInvalidAdmission},
		{"decision cannot be admitted", mut(func(r *AdmissionRequest) {
			r.Decisions = []SlotDecision{{NominalAt: 5, Decision: DecisionAdmitted, Reason: "x"}}
		}), ErrInvalidAdmission},
		{"done needs a reason", mut(func(r *AdmissionRequest) { r.NewStatus = "done"; r.NextRun = 0 }), ErrInvalidAdmission},
		{"active with terminal reason", mut(func(r *AdmissionRequest) { r.TerminalReason = "oops" }), ErrInvalidAdmission},
	}
	for _, c := range bad {
		if _, err := f.st.AdmitTaskOccurrence(ctx, c.req); !errors.Is(err, c.want) {
			t.Errorf("%s: error = %v, want %v", c.name, err, c.want)
		}
	}
	tooMany := admit(task, baseTime, next)
	for i := 0; i <= MaxOccurrenceDecisions; i++ {
		tooMany.Decisions = append(tooMany.Decisions, SlotDecision{NominalAt: int64(i + 1), Decision: DecisionSkipped, Reason: "gap"})
	}
	if _, err := f.st.AdmitTaskOccurrence(ctx, tooMany); !errors.Is(err, ErrInvalidAdmission) {
		t.Errorf("unbounded decisions: %v", err)
	}
	sameCounts(t, ledgerCounts(t, f.st), map[string]int{"turns": 0, "task_runs": 0, "task_occurrences": 0})
	if got, _ := f.st.GetManagedTask(ctx, task.ID); got.NextRun != baseTime || got.LastRun != 0 {
		t.Fatalf("rejected requests moved the cursor: %+v", got)
	}

	// A duplicate nominal slot under the same revision is refused even if the
	// cursor were reset (uniqueness is a database fact, not just the CAS).
	f.mustAdmit(admit(task, baseTime, next))
	mustExec(t, f.st, `UPDATE scheduled_tasks SET next_run=? WHERE id=?`, baseTime, task.ID)
	_, err := f.st.AdmitTaskOccurrence(ctx, admit(task, baseTime, next))
	wantErr(t, err, ErrOccurrenceDuplicate)
	if execErr(f.st, `INSERT INTO task_occurrences(occurrence_id,task_id,definition_revision,nominal_at,namespace_id,namespace_epoch,registration_revision,chat_jid,chat_folder,thread_id,visibility,decision,reason,created_at)
 VALUES('dup',?,1,?,'n',1,1,'tg:1','owner',0,'conversation','skipped','x',1)`, task.ID, baseTime) == nil {
		t.Fatal("unique (task, revision, nominal) not enforced")
	}
	// A new revision may use the same nominal instant.
	prompt := "v2"
	v2, err := f.st.UpdateManagedTask(ctx, "owner", task.ID, 1, TaskPatch{Prompt: &prompt, NextRun: ptr(baseTime)})
	if err != nil {
		t.Fatal(err)
	}
	mustExec(t, f.st, `UPDATE task_runs SET execution_state='failed', settled_at=1`)
	mustExec(t, f.st, `UPDATE task_attempts SET execution_state='failed'`)
	f.mustAdmit(admit(v2, baseTime, next))
}

func ptr[T any](v T) *T { return &v }

func TestAdmitStateGuards(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	paused, err := f.st.SetManagedTaskStatus(ctx, "owner", task.ID, 1, "paused", 0)
	if err != nil {
		t.Fatal(err)
	}
	// A tick holding the pre-pause revision conflicts; one with the new
	// revision sees an inactive task.
	_, err = f.st.AdmitTaskOccurrence(ctx, admit(task, baseTime, baseTime+1000))
	wantErr(t, err, ErrRevisionConflict)
	_, err = f.st.AdmitTaskOccurrence(ctx, admit(paused, baseTime, baseTime+1000))
	wantErr(t, err, ErrTaskNotActive)

	if err = f.st.DeleteManagedTask(ctx, "owner", task.ID, paused.Revision, "deleted_by_owner"); err != nil {
		t.Fatal(err)
	}
	dead, _ := f.st.GetManagedTask(ctx, task.ID)
	stale := admit(dead, baseTime, baseTime+1000)
	stale.ExpectedNextRun = baseTime
	_, err = f.st.AdmitTaskOccurrence(ctx, stale)
	wantErr(t, err, ErrTaskTombstoned)

	legacyID, _ := f.st.CreateTask(ctx, TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1h", NextRun: baseTime})
	req := admit(task, baseTime, baseTime+1000)
	req.TaskID = legacyID
	_, err = f.st.AdmitTaskOccurrence(ctx, req)
	wantErr(t, err, ErrNotManagedTask)
}

func TestAdmitRevalidatesScope(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	task := f.create(f.agentSpec())
	if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.AdmitTaskOccurrence(ctx, admit(task, baseTime, baseTime+1000))
	wantErr(t, err, ErrStaleScope)
	sameCounts(t, ledgerCounts(t, f.st), map[string]int{"turns": 0, "task_runs": 0, "task_occurrences": 0, "delivery_groups": 0})
	// A reminder fails closed the same way; nothing is published to a stale scope.
	rem := f.reminderSpec()
	rem.Conversation = f.conv
	if _, err = f.st.CreateManagedTask(ctx, rem); err == nil {
		t.Fatal("stale scope accepted at creation")
	}
	fresh, _ := f.st.SnapshotScope(ctx, "owner")
	rebound, err := f.st.RebindManagedTaskScope(ctx, "owner", task.ID, 1, fresh)
	if err != nil {
		t.Fatal(err)
	}
	f.mustAdmit(admit(rebound, baseTime, baseTime+1000))
}

func TestAdmitExpiryMaxRunsAndReminderExpiry(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	spec := f.agentSpec()
	spec.ExpiresAt, spec.MaxRuns, spec.Overlap, spec.QueueLimit = baseTime+5000, 2, OverlapQueue, 5
	task := f.create(spec)
	_, err := f.st.AdmitTaskOccurrence(ctx, admit(task, baseTime+5000, baseTime+6000))
	wantErr(t, err, ErrTaskExpired) // the slot is at or past the definition's expiry
	req := admit(task, baseTime, baseTime+1000)
	f.mustAdmit(req)
	cur, _ := f.st.GetManagedTask(ctx, task.ID)
	f.mustAdmit(admit(cur, baseTime+1000, baseTime+2000))
	cur, _ = f.st.GetManagedTask(ctx, task.ID)
	_, err = f.st.AdmitTaskOccurrence(ctx, admit(cur, baseTime+2000, baseTime+3000))
	wantErr(t, err, ErrMaxRunsReached)
	// Skips do not consume max_runs; a planner finishes the task truthfully.
	fin := admit(cur, 0, 0)
	fin.NominalAt, fin.NextRun, fin.NewStatus, fin.TerminalReason = 0, 0, "done", "max_runs_reached"
	fin.Decisions = []SlotDecision{{NominalAt: baseTime + 2000, Decision: DecisionSkipped, Reason: "max_runs"}}
	f.mustAdmit(fin)

	// Expired definition.
	spec2 := f.agentSpec()
	spec2.ExpiresAt = baseTime + 100
	exp := f.create(spec2)
	req = admit(exp, baseTime+200, baseTime+1000)
	req.ExpectedNextRun = exp.NextRun
	_, err = f.st.AdmitTaskOccurrence(ctx, req)
	wantErr(t, err, ErrTaskExpired)

	// A reminder older than its default 24h expiry is not published late.
	rem := f.create(f.reminderSpec())
	late := admit(rem, baseTime, 0)
	late.Now = baseTime + 25*3_600_000
	late.NewStatus, late.TerminalReason = "done", "completed_once"
	_, err = f.st.AdmitTaskOccurrence(ctx, late)
	wantErr(t, err, ErrOccurrenceExpired)
	late.NominalAt = 0
	late.Decisions = []SlotDecision{{NominalAt: baseTime, Decision: DecisionExpired, Reason: "reminder_expired"}}
	late.TerminalReason = "expired"
	f.mustAdmit(late)
	occ, _ := f.st.ListTaskOccurrences(ctx, rem.ID, 10)
	if len(occ) != 1 || occ[0].Decision != DecisionExpired || occ[0].RunID != "" {
		t.Fatalf("expired reminder: %+v", occ)
	}
}

func TestAdmitRecordsSkippedCoalescedExpiredDecisions(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	spec := f.agentSpec()
	spec.Misfire = MisfireCoalesce
	task := f.create(spec)
	req := admit(task, baseTime+3000, baseTime+4000)
	req.Decisions = []SlotDecision{
		{NominalAt: baseTime, Decision: DecisionExpired, Reason: "past_grace"},
		{NominalAt: baseTime + 1000, Decision: DecisionSkipped, Reason: "misfire_skip"},
		{NominalAt: baseTime + 2000, Decision: DecisionCoalesced, Reason: "coalesced_latest"},
	}
	req.Now = baseTime + 3500
	a := f.mustAdmit(req)
	occ, _ := f.st.ListTaskOccurrences(ctx, task.ID, 10)
	if len(occ) != 4 {
		t.Fatalf("occurrences: %+v", occ)
	}
	byAt := map[int64]TaskOccurrence{}
	for _, o := range occ {
		byAt[o.NominalAt] = o
	}
	if byAt[baseTime].Decision != DecisionExpired || byAt[baseTime+1000].Decision != DecisionSkipped || byAt[baseTime+2000].Decision != DecisionCoalesced ||
		byAt[baseTime+3000].Decision != DecisionAdmitted || byAt[baseTime+3000].RunID != a.RunID || byAt[baseTime].RunID != "" {
		t.Fatalf("decisions: %+v", byAt)
	}
	// Decision rows are immutable evidence.
	if execErr(f.st, `UPDATE task_occurrences SET decision='admitted'`) == nil {
		t.Fatal("occurrence rewritten")
	}
	// Decisions only (everything skipped): the cursor moves, nothing runs.
	again, _ := f.st.GetManagedTask(ctx, task.ID)
	skip := admit(again, 0, baseTime+9000)
	skip.NominalAt, skip.Now = 0, baseTime+8500
	skip.Decisions = []SlotDecision{{NominalAt: baseTime + 4000, Decision: DecisionSkipped, Reason: "overlap_skip"}}
	skip.NextRun = baseTime + 9000
	// The first run is still queued and the policy is not queue: admission of a
	// new slot would be refused, but pure decisions are fine.
	f.mustAdmit(skip)
	if got, _ := f.st.GetManagedTask(ctx, task.ID); got.NextRun != baseTime+9000 {
		t.Fatalf("cursor: %+v", got)
	}
	if countRows(t, f.st, `SELECT count(*) FROM task_runs`) != 1 {
		t.Fatal("skip created a run")
	}
}

func TestAdmitOverlapPolicies(t *testing.T) {
	type step struct {
		policy  string
		limit   int
		prep    func(t *testing.T, f *deliveryFixture, runID, attemptID string)
		wantErr error
	}
	reserved := func(t *testing.T, f *deliveryFixture, runID, attemptID string) {
		mustExec(t, f.st, `UPDATE task_attempts SET execution_state='reserved', provider='claude', reservation_state='held' WHERE attempt_id=?`, attemptID)
	}
	running := func(t *testing.T, f *deliveryFixture, runID, attemptID string) {
		reserved(t, f, runID, attemptID)
		mustExec(t, f.st, `UPDATE task_attempts SET execution_state='running' WHERE attempt_id=?`, attemptID)
		mustExec(t, f.st, `UPDATE task_runs SET execution_state='running' WHERE run_id=?`, runID)
	}
	unknown := func(t *testing.T, f *deliveryFixture, runID, attemptID string) {
		running(t, f, runID, attemptID)
		mustExec(t, f.st, `UPDATE task_attempts SET execution_state='execution_unknown' WHERE attempt_id=?`, attemptID)
		mustExec(t, f.st, `UPDATE task_runs SET execution_state='execution_unknown' WHERE run_id=?`, runID)
	}
	none := func(*testing.T, *deliveryFixture, string, string) {}
	cases := map[string]step{
		"skip with queued":              {OverlapSkip, 1, none, ErrTaskOccupied},
		"skip with running":             {OverlapSkip, 1, running, ErrTaskOccupied},
		"skip with unknown":             {OverlapSkip, 1, unknown, ErrTaskOccupied},
		"coalesce with queued":          {OverlapCoalesce, 1, none, ErrTaskOccupied},
		"coalesce with running":         {OverlapCoalesce, 1, running, nil},
		"queue below limit":             {OverlapQueue, 2, none, nil},
		"queue at limit":                {OverlapQueue, 1, none, ErrQueueFull},
		"queue behind running":          {OverlapQueue, 1, running, nil},
		"queue behind unknown":          {OverlapQueue, 1, unknown, nil},
		"queue reserved counts as busy": {OverlapQueue, 1, reserved, nil},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			f := newDeliveryFixture(t)
			spec := f.agentSpec()
			spec.Overlap, spec.QueueLimit = c.policy, c.limit
			task := f.create(spec)
			first := f.mustAdmit(admit(task, baseTime, baseTime+1000))
			c.prep(t, f, first.RunID, first.AttemptID)
			cur, _ := f.st.GetManagedTask(t.Context(), task.ID)
			_, err := f.st.AdmitTaskOccurrence(t.Context(), admit(cur, baseTime+1000, baseTime+2000))
			if !errors.Is(err, c.wantErr) || (c.wantErr == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, c.wantErr)
			}
			after, _ := f.st.GetManagedTask(t.Context(), task.ID)
			if c.wantErr != nil && after.NextRun != baseTime+1000 {
				t.Fatalf("refused admission advanced the cursor: %+v", after)
			}
		})
	}
}

func TestAdmitCoalesceSupersedesOnlyProvenUnstartedRuns(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	spec := f.agentSpec()
	spec.Overlap = OverlapCoalesce
	task := f.create(spec)
	first := f.mustAdmit(admit(task, baseTime, baseTime+1000))
	cur, _ := f.st.GetManagedTask(ctx, task.ID)
	req := admit(cur, baseTime+1000, baseTime+2000)
	req.SupersedeQueued = true
	second := f.mustAdmit(req)
	old, _ := f.st.GetTaskRunRecord(ctx, first.RunID)
	oldTurn, _ := f.st.GetTurn(ctx, first.TurnID)
	if old.State != ExecCancelled || old.StopReason != "superseded" || oldTurn.Status != TurnStopped {
		t.Fatalf("superseded run: %+v turn=%+v", old, oldTurn)
	}
	// The displaced durable turn cannot be recovered or dispatched.
	unfinished, _ := f.st.ListUnfinishedTurns(ctx)
	if len(unfinished) != 1 || unfinished[0].ID != second.TurnID {
		t.Fatalf("recoverable turns: %+v", unfinished)
	}
	// A turn that started meanwhile is never replaced.
	mustExec(t, f.st, `UPDATE turns SET status='running', attempts=1 WHERE id=?`, second.TurnID)
	cur, _ = f.st.GetManagedTask(ctx, task.ID)
	req = admit(cur, baseTime+2000, baseTime+3000)
	req.SupersedeQueued = true
	_, err := f.st.AdmitTaskOccurrence(ctx, req)
	wantErr(t, err, ErrTaskOccupied)
	if r, _ := f.st.GetTaskRunRecord(ctx, second.RunID); r.State != ExecQueued {
		t.Fatalf("started run was touched: %+v", r)
	}
}

func TestConcurrentAdmissionExactlyOneWins(t *testing.T) {
	for _, kind := range []string{"agent", "reminder"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t)
			spec := f.agentSpec()
			if kind == "reminder" {
				spec = f.reminderSpec()
			}
			task := f.create(spec)
			req := admit(task, baseTime, baseTime+1000)
			const callers = 12
			var wg sync.WaitGroup
			start := make(chan struct{})
			errs := make([]error, callers)
			for i := range errs {
				wg.Add(1)
				go func() {
					defer wg.Done()
					<-start
					_, errs[i] = f.st.AdmitTaskOccurrence(context.Background(), req)
				}()
			}
			close(start)
			wg.Wait()
			wins := 0
			for _, e := range errs {
				switch {
				case e == nil:
					wins++
				case !errors.Is(e, ErrCursorConflict):
					t.Errorf("loser error = %v, want ErrCursorConflict", e)
				}
			}
			if wins != 1 {
				t.Fatalf("%d callers were admitted", wins)
			}
			c := ledgerCounts(t, f.st)
			wantRuns, wantDeliveries := 1, 0
			if kind == "reminder" {
				wantRuns, wantDeliveries = 0, 1
			}
			if c["task_runs"] != wantRuns || c["turns"] != wantRuns || c["task_attempts"] != wantRuns || c["delivery_groups"] != wantDeliveries || c["task_occurrences"] != 1 {
				t.Fatalf("counts: %v", c)
			}
			if got, _ := f.st.GetManagedTask(t.Context(), task.ID); got.NextRun != baseTime+1000 {
				t.Fatalf("cursor: %+v", got)
			}
		})
	}
}

func TestConcurrentAdmissionAgainstUpdateNeverPartiallyApplies(t *testing.T) {
	for i := 0; i < 20; i++ {
		f := newDeliveryFixture(t)
		task := f.create(f.agentSpec())
		var wg sync.WaitGroup
		var admitErr, pauseErr error
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, admitErr = f.st.AdmitTaskOccurrence(context.Background(), admit(task, baseTime, baseTime+1000))
		}()
		go func() {
			defer wg.Done()
			_, pauseErr = f.st.SetManagedTaskStatus(context.Background(), "owner", task.ID, 1, "paused", 0)
		}()
		wg.Wait()
		runs := countRows(t, f.st, `SELECT count(*) FROM task_runs`)
		got, _ := f.st.GetManagedTask(t.Context(), task.ID)
		switch {
		case admitErr == nil && runs == 1 && got.NextRun == baseTime+1000:
			// admitted first; the pause (same expected revision) then also applies
			if pauseErr != nil {
				t.Fatalf("pause after admission: %v", pauseErr)
			}
		case admitErr != nil && runs == 0 && got.NextRun == baseTime && pauseErr == nil:
			wantErr(t, admitErr, ErrRevisionConflict)
		default:
			t.Fatalf("inconsistent: admit=%v pause=%v runs=%d task=%+v", admitErr, pauseErr, runs, got)
		}
	}
}

func TestAdmitRollsBackCompletelyAtEveryInsertedRow(t *testing.T) {
	decisions := []SlotDecision{
		{NominalAt: baseTime - 2000, Decision: DecisionExpired, Reason: "past_grace"},
		{NominalAt: baseTime - 1000, Decision: DecisionSkipped, Reason: "misfire_skip"},
	}
	cases := map[string][]string{
		"agent":    {"decision:0", "decision:1", "decisions", "run", "turn", "attempt", "occurrence", "cursor"},
		"reminder": {"decision:0", "decision:1", "decisions", "outbox", "occurrence", "cursor"},
	}
	for kind, stages := range cases {
		for _, stage := range stages {
			t.Run(kind+"/"+stage, func(t *testing.T) {
				f := newDeliveryFixture(t)
				spec := f.agentSpec()
				if kind == "reminder" {
					spec = f.reminderSpec()
				}
				task := f.create(spec)
				before := ledgerCounts(t, f.st)
				req := admit(task, baseTime, baseTime+1000)
				req.Decisions = decisions
				_, err := f.st.AdmitTaskOccurrence(failAt(stage), req)
				if err == nil {
					t.Fatalf("failpoint %s did not fire", stage)
				}
				sameCounts(t, ledgerCounts(t, f.st), before)
				if got, _ := f.st.GetManagedTask(t.Context(), task.ID); got.NextRun != baseTime || got.LastRun != 0 || got.Status != "active" {
					t.Fatalf("partial cursor advance: %+v", got)
				}
				// The same request then succeeds: nothing was consumed.
				f.mustAdmit(req)
			})
		}
	}
}

func TestAdmitInsideOuterTransactionRollsBackToSavepoint(t *testing.T) {
	f := newDeliveryFixture(t)
	task := f.create(f.agentSpec())
	tx, err := f.st.db.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	outer := context.WithValue(context.Background(), taskTransactionKey{}, taskTransaction{owner: f.st, tx: tx})
	failing := context.WithValue(outer, admitFailpointKey{}, func(s string) error {
		if s == "attempt" {
			return errors.New("injected")
		}
		return nil
	})
	if _, err = f.st.AdmitTaskOccurrence(failing, admit(task, baseTime, baseTime+1000)); err == nil {
		t.Fatal("failpoint did not fire")
	}
	var runs, turns int
	if err = tx.QueryRow(`SELECT (SELECT count(*) FROM task_runs),(SELECT count(*) FROM turns)`).Scan(&runs, &turns); err != nil || runs != 0 || turns != 0 {
		t.Fatalf("savepoint leaked rows: runs=%d turns=%d err=%v", runs, turns, err)
	}
	// The outer transaction is still usable and can admit successfully.
	if _, err = f.st.AdmitTaskOccurrence(outer, admit(task, baseTime, baseTime+1000)); err != nil {
		t.Fatalf("admission inside the outer transaction: %v", err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM task_runs`) != 1 {
		t.Fatal("outer commit lost the run")
	}
}

func TestAdmitUsesInjectedClockNotWallClock(t *testing.T) {
	f := newDeliveryFixture(t)
	task := f.create(f.agentSpec())
	req := admit(task, baseTime, baseTime+1000)
	a := f.mustAdmit(req)
	occ, _ := f.st.ListTaskOccurrences(t.Context(), task.ID, 1)
	if occ[0].CreatedAt != req.Now || a.Task.LastRun != req.Now || time.Now().UnixMilli() == req.Now {
		t.Fatalf("clock: %+v", occ[0])
	}
}

func TestAdmitRejectsRepeatedWallSlotKey(t *testing.T) {
	f := newDeliveryFixture(t)
	task := f.create(f.agentSpec())
	req := admit(task, baseTime, baseTime+3_600_000)
	req.WallSlotKey = "2027-01-01T02:30/Europe/Belgrade"
	f.mustAdmit(req)
	cur, _ := f.st.GetManagedTask(t.Context(), task.ID)
	mustExec(t, f.st, `UPDATE task_runs SET execution_state='failed', settled_at=1`)
	mustExec(t, f.st, `UPDATE task_attempts SET execution_state='failed'`)
	again := admit(cur, baseTime+3_600_000, baseTime+7_200_000)
	again.WallSlotKey = req.WallSlotKey
	_, err := f.st.AdmitTaskOccurrence(t.Context(), again)
	wantErr(t, err, ErrOccurrenceDuplicate)
	if countRows(t, f.st, `SELECT count(*) FROM task_runs`) != 1 {
		t.Fatal("duplicate wall slot created a run")
	}
}
