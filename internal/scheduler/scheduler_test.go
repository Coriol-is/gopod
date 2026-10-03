package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestComputeNextRunCron(t *testing.T) {
	now := time.Now().UnixMilli()
	task := store.TaskRecord{ScheduleType: "cron", ScheduleValue: "* * * * *"} // every minute

	next, status := computeNextRun(task, now)
	if status != "active" {
		t.Errorf("status = %q, want active", status)
	}
	if next <= now {
		t.Errorf("next_run %d should be > now %d", next, now)
	}
	// "every minute" cron — next should be within 60s of now.
	if next-now > 61_000 {
		t.Errorf("next_run %d is more than 61s after now %d", next, now)
	}
}

func TestComputeNextRunInterval(t *testing.T) {
	now := int64(1000000)
	task := store.TaskRecord{ScheduleType: "interval", ScheduleValue: "1h"}

	next, status := computeNextRun(task, now)
	if status != "active" {
		t.Errorf("status = %q", status)
	}
	expected := now + time.Hour.Milliseconds()
	if next != expected {
		t.Errorf("next = %d, want %d", next, expected)
	}
}

func TestComputeNextRunOnce(t *testing.T) {
	task := store.TaskRecord{ScheduleType: "once"}
	next, status := computeNextRun(task, 1000)
	if status != "done" || next != 0 {
		t.Errorf("once: next=%d status=%q, want 0/done", next, status)
	}
}

func TestValidateCron(t *testing.T) {
	if err := ValidateCron("0 9 * * *"); err != nil {
		t.Errorf("valid cron rejected: %v", err)
	}
	if err := ValidateCron("not a cron"); err == nil {
		t.Error("invalid cron accepted")
	}
}

func TestValidateInterval(t *testing.T) {
	if err := ValidateInterval("1h30m"); err != nil {
		t.Errorf("valid interval rejected: %v", err)
	}
	if err := ValidateInterval("banana"); err == nil {
		t.Error("invalid interval accepted")
	}
}

func TestTaskSourceIDRoundTrip(t *testing.T) {
	sid := taskSourceID("abc-123", 1700000000000)
	id, runAt, ok := parseTaskSourceID(sid)
	if !ok || id != "abc-123" || runAt != 1700000000000 {
		t.Errorf("parse(%q) = (%q, %d, %v)", sid, id, runAt, ok)
	}
	if _, _, ok := parseTaskSourceID("garbage"); ok {
		t.Error("garbage parsed as ok")
	}
}

func newSchedulerTestStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

// storeAdapter is a minimal queue.TurnStore over *store.Store for
// tests. cmd/gopod has the production copy.
type storeAdapter struct{ s *store.Store }

func (a storeAdapter) InsertTurn(ctx context.Context, it queue.Item) (int64, bool, error) {
	return a.s.InsertTurn(ctx, store.Turn{
		Source: it.Source, SourceID: it.SourceID, ChatFolder: it.Folder, ChatID: it.ChatID,
		TGMessageID: int64(it.MessageID), IsOwner: it.IsOwner, IsVoice: it.IsVoice,
		Text: it.Text, FilePath: it.FilePath,
	})
}
func (a storeAdapter) MarkRunning(ctx context.Context, id int64) error {
	return a.s.MarkTurnRunning(ctx, id)
}
func (a storeAdapter) MarkFinished(ctx context.Context, id int64, status, e string) error {
	return a.s.MarkTurnFinished(ctx, id, status, e)
}
func (a storeAdapter) LoadUnfinished(ctx context.Context) ([]queue.Item, error) { return nil, nil }

func TestRunTaskLogsRunningThenFinishes(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	taskID, err := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "do it", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: 1})
	if err != nil {
		t.Fatal(err)
	}
	finished := make(chan struct{})
	handler := func(ctx context.Context, items []queue.Item) error { return nil }
	q := queue.New(ctx, handler, 1, storeAdapter{st}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := New(st, q, nil)
	q.OnDone(func(it queue.Item, err error) { close(finished) })

	task, _ := st.GetTask(ctx, taskID)
	now := int64(5000)
	s.runTask(ctx, task, now)

	<-finished
	// Give the scheduler's own OnDone hook (registered first in New)
	// time to write; it runs before ours, so the row is final here.
	run, err := st.GetTaskRun(ctx, taskID, now)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" || run.TurnID == 0 {
		t.Errorf("run = %+v, want success with turn_id", run)
	}
	turn, _ := st.GetTurn(ctx, run.TurnID)
	if turn.Source != "task" || turn.Status != store.TurnDone || turn.ChatID != 0 {
		t.Errorf("turn = %+v", turn)
	}
}

func TestRunTaskDupMarksSkipped(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	taskID, _ := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: 1})
	task, _ := st.GetTask(ctx, taskID)
	block := make(chan struct{})
	handler := func(ctx context.Context, items []queue.Item) error { <-block; return nil }
	q := queue.New(ctx, handler, 1, storeAdapter{st}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	s := New(st, q, nil)
	s.runTask(ctx, task, 5000)
	s.runTask(ctx, task, 5000) // same slot again, as after a crash mid-poll
	close(block)
	// The second log row would collide on (task_id, run_at); runTask
	// must not insert a second one, and must leave the first intact.
	run, err := st.GetTaskRun(ctx, taskID, 5000)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status == "skipped" {
		t.Error("first run row overwritten by the duplicate")
	}
}
