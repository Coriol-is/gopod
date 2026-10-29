package store

import (
	"errors"
	"testing"
)

func TestProvisionalReportIsModelReportedAndNeverTouchesExecutionState(t *testing.T) {
	f := newDeliveryFixture(t)
	a := startedAttempt(t, f)
	ctx := t.Context()
	before, _ := f.st.GetTaskRunRecord(ctx, a.RunID)
	err := f.st.RecordProvisionalReport(ctx, ProvisionalReport{
		RunID: a.RunID, AttemptID: a.AttemptID, Status: GoalAchieved,
		OutcomeJSON:    `{"status":"achieved","checks":[],"evidence":["/etc/passwd"],"limitations":[]}`,
		CheckpointJSON: `{"planVersion":2,"lastSupportedMilestone":"m","nextSafeStep":"n","unresolvedEffects":""}`, PlanVersion: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := f.st.GetTaskRunRecord(ctx, a.RunID)
	if run.GoalStatus != GoalAchieved || run.GoalAuthority != "model_reported" {
		t.Fatalf("goal = %s/%s", run.GoalStatus, run.GoalAuthority)
	}
	if run.State != before.State || run.PublicationState != before.PublicationState || run.ResultID != before.ResultID {
		t.Fatalf("a report moved execution/publication state: %+v -> %+v", before, run)
	}
	var verifications int
	if err := f.st.db.QueryRowContext(ctx, `SELECT count(*) FROM task_verifications`).Scan(&verifications); err != nil || verifications != 0 {
		t.Fatalf("a report minted host verification: %d %v", verifications, err)
	}
}

func TestProvisionalReportRejectsNonLiveAndStale(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	// Reserved but never started: not live.
	_, q := admitted(t, f)
	if _, err := f.st.ReserveTaskAttempt(ctx, q.AttemptID, testSnap, baseTime); err != nil {
		t.Fatal(err)
	}
	rep := ProvisionalReport{RunID: q.RunID, AttemptID: q.AttemptID, Status: GoalPartial, OutcomeJSON: `{"status":"partial"}`}
	if err := f.st.RecordProvisionalReport(ctx, rep); !errors.Is(err, ErrReportRejected) {
		t.Fatalf("reserved attempt: %v", err)
	}
	if err := f.st.BeginTaskSpawn(ctx, q.AttemptID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordProvisionalReport(ctx, ProvisionalReport{RunID: q.RunID, AttemptID: q.AttemptID, CheckpointJSON: `{"planVersion":5}`, PlanVersion: 5}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordProvisionalReport(ctx, ProvisionalReport{RunID: q.RunID, AttemptID: q.AttemptID, CheckpointJSON: `{"planVersion":4}`, PlanVersion: 4}); !errors.Is(err, ErrReportRejected) {
		t.Fatalf("stale checkpoint: %v", err)
	}
	// Wrong run for the attempt, unknown attempt, empty report.
	for name, r := range map[string]ProvisionalReport{
		"foreign run": {RunID: "other", AttemptID: q.AttemptID, OutcomeJSON: `{}`},
		"unknown":     {RunID: q.RunID, AttemptID: "nope", OutcomeJSON: `{}`},
		"empty":       {RunID: q.RunID, AttemptID: q.AttemptID},
		"bad status":  {RunID: q.RunID, AttemptID: q.AttemptID, Status: "delivered", OutcomeJSON: `{}`},
	} {
		if err := f.st.RecordProvisionalReport(ctx, r); !errors.Is(err, ErrReportRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Settled attempts cannot write (late report).
	if _, err := f.st.SettleTaskAttempt(ctx, q.AttemptID, Settlement{Kind: SettleExited, Evidence: "exec_exit_observed code=0"}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordProvisionalReport(ctx, rep); !errors.Is(err, ErrReportRejected) {
		t.Fatalf("late report: %v", err)
	}
}
