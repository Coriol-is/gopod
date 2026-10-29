package store

import (
	"context"
	"errors"
	"fmt"
	"testing"
)

// baseTime is a fake-clock origin; admission uses AdmissionRequest.Now only.
const baseTime = int64(1_000_000_000_000)

func (f *deliveryFixture) agentSpec() ManagedTask {
	return ManagedTask{
		Kind: TaskKindAgent, Conversation: f.conv, ActorRef: "owner:service", Prompt: "run the report",
		ScheduleType: "interval", ScheduleValue: "1h", Timezone: "UTC", NextRun: baseTime,
	}
}

func (f *deliveryFixture) reminderSpec() ManagedTask {
	return ManagedTask{
		Kind: TaskKindReminder, Conversation: f.conv, ActorRef: "owner:service", LiteralText: "stand up",
		ScheduleType: "once", ScheduleValue: "2001-09-09T01:46:40Z", Timezone: "Europe/Belgrade", NextRun: baseTime,
	}
}

func (f *deliveryFixture) create(spec ManagedTask) ManagedTask {
	f.t.Helper()
	got, err := f.st.CreateManagedTask(f.t.Context(), spec)
	if err != nil {
		f.t.Fatalf("CreateManagedTask: %v", err)
	}
	return got
}

// admit builds a request admitting nominal and moving the cursor to next.
func admit(task ManagedTask, nominal, next int64) AdmissionRequest {
	return AdmissionRequest{
		TaskID: task.ID, ExpectedRevision: task.Revision, ExpectedNextRun: task.NextRun,
		Now: nominal + 10, NominalAt: nominal, NextRun: next,
	}
}

func (f *deliveryFixture) mustAdmit(req AdmissionRequest) Admission {
	f.t.Helper()
	a, err := f.st.AdmitTaskOccurrence(f.t.Context(), req)
	if err != nil {
		f.t.Fatalf("AdmitTaskOccurrence: %v", err)
	}
	return a
}

// ledgerCounts is the row count of every table an admission can write.
func ledgerCounts(t *testing.T, st *Store) map[string]int {
	t.Helper()
	out := map[string]int{}
	for _, tbl := range []string{"turns", "task_runs", "task_attempts", "task_occurrences", "task_verifications",
		"delivery_groups", "outbox_parts", "ipc_message_requests", "conversation_sequences"} {
		out[tbl] = countRows(t, st, "SELECT count(*) FROM "+tbl)
	}
	return out
}

func sameCounts(t *testing.T, got, want map[string]int) {
	t.Helper()
	for k, w := range want {
		if got[k] != w {
			t.Errorf("table %s has %d rows, want %d", k, got[k], w)
		}
	}
}

func mustExec(t *testing.T, st *Store, query string, args ...any) {
	t.Helper()
	if _, err := st.db.ExecContext(context.Background(), query, args...); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
}

func execErr(st *Store, query string, args ...any) error {
	_, err := st.db.ExecContext(context.Background(), query, args...)
	return err
}

func failAt(stage string) context.Context {
	return context.WithValue(context.Background(), admitFailpointKey{}, func(s string) error {
		if s == stage {
			return fmt.Errorf("injected failure at %s", stage)
		}
		return nil
	})
}

func wantErr(t *testing.T, got, want error) {
	t.Helper()
	if !errors.Is(got, want) {
		t.Fatalf("error = %v, want %v", got, want)
	}
}
