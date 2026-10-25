package telegram

import (
	"context"
	"fmt"
	"github.com/Coriol-is/gopod/internal/control"
	"github.com/Coriol-is/gopod/internal/store"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestTasksPanelSlashPagesAndToggle(t *testing.T) {
	b, fake, r := newCommandFixture(t)
	ctx := context.Background()
	control.RegisterTaskCommands(r, b.store, func(id int64) string {
		if id == streamChat {
			return "owner"
		}
		return "grp"
	}, nil)
	for i := 0; i < 4; i++ {
		_, err := b.store.CreateTask(ctx, store.TaskRecord{ID: fmt.Sprintf("task-%d", i), ChatFolder: "owner", ChatJID: buildChatJID(streamChat), Prompt: strings.Repeat("я", 29) + "🙂" + strings.Repeat("ю", 80), ScheduleType: "interval", ScheduleValue: "1h", NextRun: time.Now().Add(time.Hour).UnixMilli(), CreatedAt: int64(i + 1)})
		if err != nil {
			t.Fatal(err)
		}
	}
	b.dispatchSlash(ctx, slashMsg(streamChat, 7, "/tasks"), "/tasks")
	sent := lastCall(t, fake, "sendMessage")
	if !utf8.ValidString(sent.Form["text"]) || !strings.Contains(sent.Form["text"], "1/2") || sent.Form["message_thread_id"] != "7" {
		t.Fatalf("bad tasks response: %v", sent.Form)
	}
	pause := buttonData(t, sent, "1 · Pause")
	b.callbackHandler(ctx, b.api, cbUpdate("task-pause", streamChat, 7, 500, pause))
	answeredOnce(t, fake, "task-pause")
	edit := lastCall(t, fake, "editMessageText")
	record, err := b.store.GetTask(ctx, "task-0")
	if err != nil || record.Status != "paused" {
		t.Fatal("pause callback failed")
	}
	resume := buttonData(t, edit, "1 · Resume")
	b.callbackHandler(ctx, b.api, cbUpdate("task-resume", streamChat, 7, 500, resume))
	answeredOnce(t, fake, "task-resume")
	record, err = b.store.GetTask(ctx, "task-0")
	if err != nil || record.Status != "active" || record.NextRun <= time.Now().UnixMilli() {
		t.Fatal("resume callback failed")
	}
	b.callbackHandler(ctx, b.api, cbUpdate("task-next", streamChat, 7, 500, buttonData(t, sent, "Next ›")))
	answeredOnce(t, fake, "task-next")
	edit = lastCall(t, fake, "editMessageText")
	if !strings.Contains(edit.Form["text"], "2/2") || strings.Contains(edit.Form["text"], "task-0") {
		t.Fatal("task page did not change")
	}
	buttonData(t, edit, "4 · Pause")
}
