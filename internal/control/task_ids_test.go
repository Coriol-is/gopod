package control

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// IPC agents may choose short explicit task ids. Every public task management
// path must accept them, display a usable id and preserve chat-local scope.
func TestTasksExplicitShortIDs(t *testing.T) {
	for _, id := range []string{"x", "daily7"} {
		t.Run(id, func(t *testing.T) {
			st := testStore(t)
			ctx := context.Background()
			next := time.Now().Add(time.Hour).UnixMilli()
			if _, err := st.CreateTask(ctx, store.TaskRecord{ID: id, ChatFolder: "owner", ChatJID: "tg:100", Prompt: "test recurring reminder", ScheduleType: "interval", ScheduleValue: "1h", NextRun: next}); err != nil {
				t.Fatal(err)
			}
			r := New(silentLog())
			RegisterTaskCommands(r, st, func(chatID int64) string {
				if chatID == 100 {
					return "owner"
				}
				return "other"
			}, nil)
			call := func(chatID int64, args ...string) Response {
				t.Helper()
				resp, err := r.Dispatch(ctx, Command{Name: "tasks", Args: args, Caller: Caller{Source: SourceTelegram, ChatID: chatID, IsOwner: chatID == 100}})
				if err != nil {
					t.Fatal(err)
				}
				return resp
			}
			if resp := call(100, "list"); resp.Code != 0 || !strings.Contains(resp.Text, id) {
				t.Fatalf("list lost short id: %+v", resp)
			}
			if resp := call(200, "list"); resp.Code != 0 || strings.Contains(resp.Text, "test recurring reminder") {
				t.Fatalf("foreign list leaked task: %+v", resp)
			}
			for _, op := range []string{"pause", "resume", "cancel"} {
				if resp := call(200, op, id); resp.Code == 0 {
					t.Fatalf("foreign %s accepted", op)
				}
				got, err := st.GetTask(ctx, id)
				if err != nil || got.Status != "active" || got.NextRun != next {
					t.Fatalf("foreign %s changed task: %+v %v", op, got, err)
				}
			}
			if resp := call(100, "pause", id); resp.Code != 0 || !strings.Contains(resp.Text, id) {
				t.Fatalf("pause short id: %+v", resp)
			}
			got, err := st.GetTask(ctx, id)
			if err != nil || got.Status != "paused" || got.NextRun != 0 {
				t.Fatalf("pause state: %+v %v", got, err)
			}
			if resp := call(100, "resume", id); resp.Code != 0 || !strings.Contains(resp.Text, id) {
				t.Fatalf("resume short id: %+v", resp)
			}
			got, err = st.GetTask(ctx, id)
			if err != nil || got.Status != "active" || got.NextRun <= time.Now().UnixMilli() {
				t.Fatalf("resume did not schedule future occurrence: %+v %v", got, err)
			}
			if resp := call(100, "cancel", id); resp.Code != 0 || !strings.Contains(resp.Text, id) {
				t.Fatalf("cancel short id: %+v", resp)
			}
			if _, err := st.GetTask(ctx, id); !errors.Is(err, store.ErrTaskNotFound) {
				t.Fatalf("cancel did not delete task: %v", err)
			}
		})
	}
}
