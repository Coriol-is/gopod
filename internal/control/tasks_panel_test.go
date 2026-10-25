package control

import (
	"context"
	"fmt"
	"github.com/Coriol-is/gopod/internal/store"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTasksPanelPagesAndStateActions(t *testing.T) {
	st := testStore(t)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		_, err := st.CreateTask(ctx, store.TaskRecord{ID: fmt.Sprintf("task-%d", i), ChatFolder: "owner", ChatJID: "tg:100", Prompt: strings.Repeat("я🙂", 80), ScheduleType: "interval", ScheduleValue: "1h", NextRun: time.Now().Add(time.Hour).UnixMilli(), CreatedAt: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	_, err := st.CreateTask(ctx, store.TaskRecord{ID: "foreign", ChatFolder: "other", Prompt: "FOREIGN PRIVATE TASK", ScheduleType: "interval", ScheduleValue: "1h"})
	if err != nil {
		t.Fatal(err)
	}
	r := New(silentLog())
	RegisterTaskCommands(r, st, func(id int64) string {
		if id == 100 {
			return "owner"
		}
		return "other"
	}, nil)
	call := func(name string, id int64, args ...string) Response {
		t.Helper()
		resp, err := r.Dispatch(ctx, Command{Name: name, Args: args, Caller: Caller{Source: SourceTelegram, ChatID: id, IsOwner: id == 100}})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}
	first := call("tasks", 100)
	if !utf8.ValidString(first.Text) || !strings.Contains(first.Text, "1/3") || strings.Contains(first.Text, "task-3") || strings.Contains(first.Text, "FOREIGN") {
		t.Fatalf("invalid first page: %s", first.Text)
	}
	if len(first.Buttons) != 4 {
		t.Fatalf("buttons=%v", first.Buttons)
	}
	action := first.Buttons[0][0]
	paused := call(action.Command, 100, action.Args...)
	if paused.Code != 0 || !strings.Contains(paused.Text, "[paused] task-0") {
		t.Fatalf("pause panel: %+v", paused)
	}
	record, _ := st.GetTask(ctx, "task-0")
	if record.Status != "paused" || record.NextRun != 0 {
		t.Fatal("pause failed")
	}
	stale := call(action.Command, 100, action.Args...)
	if stale.Code == 0 || !strings.Contains(stale.Text, "No change made") {
		t.Fatal("old button changed state")
	}
	denied := call(action.Command, 200, action.Args...)
	if denied.Code == 0 || strings.Contains(denied.Text, "task-1") {
		t.Fatal("foreign button scope")
	}
	resume := paused.Buttons[0][0]
	resumed := call(resume.Command, 100, resume.Args...)
	record, _ = st.GetTask(ctx, "task-0")
	if resumed.Code != 0 || record.Status != "active" || record.NextRun <= time.Now().UnixMilli() {
		t.Fatal("resume failed")
	}
	next := first.Buttons[len(first.Buttons)-1]
	nextButton := next[len(next)-1]
	second := call(nextButton.Command, 100, nextButton.Args...)
	if !strings.Contains(second.Text, "2/3") || strings.Contains(second.Text, "task-0") || !strings.Contains(second.Text, "task-3") {
		t.Fatalf("page2: %s", second.Text)
	}
	last := call("tasks", 100, "list", "999")
	if !strings.Contains(last.Text, "3/3") || !strings.Contains(last.Text, "task-7") {
		t.Fatal("page clamp")
	}
	if call("tasks", 100, "list", "bad").Code == 0 {
		t.Fatal("bad page accepted")
	}
	if call("tasks.state", 100, "task-0", "active", "done", "0").Code == 0 {
		t.Fatal("invalid state transition accepted")
	}
	if err := st.UpdateTaskStatus(ctx, "task-0", "done", 0, 0); err != nil {
		t.Fatal(err)
	}
	if call("tasks", 100, "resume", "task-0").Code == 0 {
		t.Fatal("completed task resumed")
	}
}

func TestTasksPanelEmptyAndUnicodeTruncation(t *testing.T) {
	st := testStore(t)
	resp, err := tasksList(context.Background(), st, "owner")
	if err != nil || resp.Text == "" || len(resp.Buttons) == 0 {
		t.Fatal("empty tasks silent")
	}
	text := strings.Repeat("я", 29) + "🙂" + strings.Repeat("ю", 80)
	got := truncate(text, 60)
	if !utf8.ValidString(got) || len([]rune(got)) != 61 {
		t.Fatalf("invalid unicode truncation: %q", got)
	}
}
