package scheduler

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"sync/atomic"
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
	for _, bad := range []string{"garbage", ":1", "a:", "a:x"} {
		if _, _, ok := parseTaskSourceID(bad); ok {
			t.Errorf("%q parsed as ok", bad)
		}
	}
	if id, runAt, ok := parseTaskSourceID("a:b:5"); !ok || id != "a:b" || runAt != 5 {
		t.Errorf("a:b:5 = (%q, %d, %v)", id, runAt, ok)
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

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestRunTaskLogsRunningThenFinishes(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	const slot = int64(1)
	taskID, err := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "do it", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: slot})
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	finished := make(chan struct{})
	handler := func(ctx context.Context, items []queue.Item) error { <-release; return nil }
	q := queue.New(ctx, handler, 1, storeAdapter{st}, discardLogger())
	t.Cleanup(q.Close)
	s := New(st, q, discardLogger())
	q.OnDone(func(it queue.Item, err error) { close(finished) })

	task, _ := st.GetTask(ctx, taskID)
	s.runTask(ctx, task, 5000)

	run, err := st.GetTaskRun(ctx, taskID, slot)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "running" || run.TurnID == 0 {
		t.Errorf("before finish: run = %+v, want running with turn_id", run)
	}

	close(release)
	<-finished // scheduler's hook was registered first, so the row is final
	run, err = st.GetTaskRun(ctx, taskID, slot)
	if err != nil {
		t.Fatal(err)
	}
	if run.Status != "success" {
		t.Errorf("after finish: run = %+v, want success", run)
	}
	turn, _ := st.GetTurn(ctx, run.TurnID)
	if turn.Source != "task" || turn.Status != store.TurnDone || turn.ChatID != 0 {
		t.Errorf("turn = %+v", turn)
	}
}

func TestRunTaskDupSlotRunsOnce(t *testing.T) {
	st := newSchedulerTestStore(t)
	ctx := context.Background()
	const slot = int64(1)
	taskID, _ := st.CreateTask(ctx, store.TaskRecord{ChatFolder: "owner", ChatJID: "tg:1", Prompt: "p", ScheduleType: "once", ScheduleValue: "2020-01-01T00:00:00Z", NextRun: slot})
	task, _ := st.GetTask(ctx, taskID)
	release := make(chan struct{})
	finished := make(chan struct{}, 2)
	var calls atomic.Int32
	handler := func(ctx context.Context, items []queue.Item) error {
		calls.Add(1)
		<-release
		return nil
	}
	q := queue.New(ctx, handler, 1, storeAdapter{st}, discardLogger())
	t.Cleanup(q.Close)
	s := New(st, q, discardLogger())
	q.OnDone(func(it queue.Item, err error) { finished <- struct{}{} })

	s.runTask(ctx, task, 5000)
	first, err := st.GetTaskRun(ctx, taskID, slot)
	if err != nil {
		t.Fatal(err)
	}
	// Same slot again with a later poll time, as after a crash between
	// Enqueue and UpdateTaskStatus: the stale pre-crash record is re-run.
	s.runTask(ctx, task, 65000)

	again, err := st.GetTaskRun(ctx, taskID, slot)
	if err != nil {
		t.Fatal(err)
	}
	if again.TurnID != first.TurnID || again.Status != "running" {
		t.Errorf("dup disturbed the row: first %+v, after %+v", first, again)
	}
	if got, _ := st.GetTask(ctx, taskID); got.Status != "done" {
		t.Errorf("task status = %q after dup, want done (advanced)", got.Status)
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("turn never finished")
	}
	if n := calls.Load(); n != 1 {
		t.Errorf("handler calls = %d, want 1", n)
	}
	final, err := st.GetTaskRun(ctx, taskID, slot)
	if err != nil {
		t.Fatal(err)
	}
	if final.TurnID == 0 || final.Status != "success" {
		t.Errorf("final row = %+v, want success with turn_id", final)
	}
}
