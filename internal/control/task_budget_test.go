package control

import (
	"context"
	"fmt"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func TestFullNonOwnerTaskBudgetRejectsBeforeParserAndOnResume(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:2", Folder: "alice", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10; i++ {
		if _, err := st.CreateTask(ctx, store.TaskRecord{ID: fmt.Sprint(i), ChatFolder: "alice", ChatJID: "tg:2", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1h"}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	resp, err := tasksAdd(ctx, st, "alice", Caller{ChatID: 2}, []string{"daily reminder"}, func(context.Context, string, string) (string, error) { calls++; return "", nil })
	if err != nil || resp.Code == 0 || calls != 0 {
		t.Fatalf("full budget spent parser call: %+v %v calls=%d", resp, err, calls)
	}
	if _, err := st.CreateTask(ctx, store.TaskRecord{ID: "old-paused", ChatFolder: "alice", ChatJID: "tg:2", Prompt: "p", ScheduleType: "interval", ScheduleValue: "1h", Status: "paused"}); err != nil {
		t.Fatal(err)
	}
	resp, err = tasksSetStatus(ctx, st, "alice", []string{"old-paused"}, "active")
	if err != nil || resp.Code == 0 {
		t.Fatalf("resume exceeded cap: %+v %v", resp, err)
	}
}
