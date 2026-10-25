package store

import (
	"context"
	"errors"
	"testing"
)

func TestTaskRequestMutationAndResultCommitAtomically(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	calls := 0
	apply := func(inner context.Context) ([]byte, error) {
		calls++
		if _, err := st.CreateTask(inner, TaskRecord{ID: "daily", ChatFolder: "alice", Prompt: "work", ScheduleType: "interval", ScheduleValue: "1h", NextRun: 100}); err != nil {
			return nil, err
		}
		return []byte(`{"taskId":"daily"}`), nil
	}
	first, err := st.ApplyTaskRequest(ctx, "alice", "request.json", "hash", 2, apply)
	if err != nil {
		t.Fatal(err)
	}
	second, err := st.ApplyTaskRequest(ctx, "alice", "request.json", "hash", 2, apply)
	if err != nil || string(second) != string(first) || calls != 1 {
		t.Fatalf("request replayed: calls=%d result=%s err=%v", calls, second, err)
	}
	if _, err := st.ApplyTaskRequest(ctx, "alice", "request.json", "different", 2, apply); !errors.Is(err, ErrTaskRequestConflict) {
		t.Fatalf("filename reuse allowed: %v", err)
	}
	_, err = st.ApplyTaskRequest(ctx, "alice", "rollback.json", "hash", 2, func(inner context.Context) ([]byte, error) {
		_, e := st.CreateTask(inner, TaskRecord{ID: "rolled-back", ChatFolder: "alice", Prompt: "work"})
		if e != nil {
			t.Fatal(e)
		}
		return nil, errors.New("response failed")
	})
	if err == nil {
		t.Fatal("failure ignored")
	}
	if _, err := st.GetTask(ctx, "rolled-back"); !errors.Is(err, ErrTaskNotFound) {
		t.Fatal("failed response committed side effect")
	}
}

func TestTaskRequestPendingResultsAreNotEvicted(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	calls := 0
	apply := func(context.Context) ([]byte, error) { calls++; return []byte(`{}`), nil }
	for _, name := range []string{"pending-a", "pending-b"} {
		if _, err := st.ApplyTaskRequest(ctx, "alice", name, "hash", 2, apply); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.ApplyTaskRequest(ctx, "alice", "blocked-new", "hash", 2, apply); err == nil {
		t.Fatal("unacknowledged backlog unbounded")
	}
	if _, err := st.ApplyTaskRequest(ctx, "alice", "pending-a", "hash", 2, apply); err != nil || calls != 2 {
		t.Fatal("pending operation lost replay authority", err, calls)
	}
	if err := st.AcknowledgeTaskRequest(ctx, "alice", "pending-b", "hash", 2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		name := string(rune('c' + i))
		if _, err := st.ApplyTaskRequest(ctx, "alice", name, "hash", 2, apply); err != nil {
			t.Fatal(err)
		}
		if err := st.AcknowledgeTaskRequest(ctx, "alice", name, "hash", 2); err != nil {
			t.Fatal(err)
		}
	}
	before := calls
	if _, err := st.ApplyTaskRequest(ctx, "alice", "pending-a", "hash", 2, apply); err != nil || calls != before {
		t.Fatal("pending result evicted by newer archived requests", err, calls, before)
	}
}
