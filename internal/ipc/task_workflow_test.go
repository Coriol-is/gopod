package ipc_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/scheduler"
	"github.com/Coriol-is/gopod/internal/store"
)

type noMessages struct{}

func (noMessages) Send(context.Context, string, string) error { return nil }

type workflow struct {
	st   *store.Store
	root string
	seq  int
	stop context.CancelFunc
}
type receipt struct {
	Status string         `json:"status"`
	Error  string         `json:"error"`
	Result ipc.TaskResult `json:"result"`
}

func newWorkflow(t *testing.T) *workflow {
	t.Helper()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(context.Background(), filepath.Join(root, "store.sqlite"), log)
	if err != nil {
		t.Fatal(err)
	}
	for i, folder := range []string{"owner", "alice", "bob"} {
		if err := st.RegisterChat(context.Background(), store.RegisteredChat{JID: fmt.Sprintf("tg:%d", i+1), Folder: folder, AddedAt: 1, IsOwner: folder == "owner"}); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(root, "ipc", folder, "tasks"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := ipc.New(ipc.Config{DataDir: root, OwnerFolder: "owner", Tick: 5 * time.Millisecond}, noMessages{}, scheduler.NewIPCSink(st), log, func(folder string) (string, bool) { r, err := st.GetByFolder(ctx, folder); return r.JID, err == nil })
	done := make(chan struct{})
	go func() { defer close(done); w.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("watcher did not stop")
		}
		st.Close()
	})
	return &workflow{st: st, root: root, stop: cancel}
}
func (f *workflow) submit(t *testing.T, folder string, payload any) receipt {
	t.Helper()
	f.seq++
	name := fmt.Sprintf("request-%04d", f.seq)
	return f.named(t, folder, name, payload)
}
func (f *workflow) named(t *testing.T, folder, name string, payload any) receipt {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(f.root, "ipc", folder, "tasks", name+".json")
	result := filepath.Join(f.root, "ipc", folder, "results", name+".result.json")
	os.Remove(result)
	tmp := input + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, input); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(result); err == nil {
			var r receipt
			if err = json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			return r
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no source-scoped IPC receipt for %s/%s (possible deadlock)", folder, name)
	return receipt{}
}
func schedule(_ string) map[string]any {
	return map[string]any{"op": "schedule", "prompt": "Review notes and save summary", "scheduleType": "cron", "scheduleValue": "CRON_TZ=Europe/Belgrade 0 9 * * *"}
}
func ok(t *testing.T, r receipt) {
	t.Helper()
	if r.Status != "ok" {
		t.Fatalf("request failed: %+v", r)
	}
}

func TestTaskIPCWorkflowUsesRealStoreAndScopedReceipts(t *testing.T) {
	f := newWorkflow(t)
	r := f.named(t, "alice", "daily-create", schedule("daily"))
	id := r.Result.TaskID
	ok(t, r)
	if r.Result.Task == nil || r.Result.Task.NextRun <= time.Now().UnixMilli() || r.Result.Task.ChatJID != "tg:2" || r.Result.Task.NextRunAt == "" {
		t.Fatalf("not runnable: %+v", r)
	}
	originalNext := r.Result.Task.NextRun
	ok(t, f.named(t, "alice", "daily-create", schedule("daily")))
	task, _ := f.st.GetTask(context.Background(), id)
	if task.NextRun != originalNext {
		t.Fatal("same schedule shifted on replay")
	}
	r = f.submit(t, "alice", map[string]any{"op": "list"})
	ok(t, r)
	if len(r.Result.Tasks) != 1 {
		t.Fatal("own list wrong")
	}
	r = f.submit(t, "bob", map[string]any{"op": "list"})
	ok(t, r)
	if len(r.Result.Tasks) != 0 {
		t.Fatal("foreign list leaked")
	}
	for _, op := range []string{"get", "pause", "resume", "update", "cancel", "delete"} {
		r = f.submit(t, "bob", map[string]any{"op": op, "taskId": id, "prompt": "foreign"})
		if r.Status != "error" {
			t.Fatalf("foreign %s allowed", op)
		}
	}
	r = f.submit(t, "owner", map[string]any{"op": "list", "targetChatFolder": "alice"})
	ok(t, r)
	if len(r.Result.Tasks) != 1 {
		t.Fatal("owner scoped list unavailable")
	}
	r = f.submit(t, "alice", map[string]any{"op": "pause", "taskId": id})
	ok(t, r)
	if r.Result.Task.Status != "paused" || r.Result.Task.NextRun != 0 {
		t.Fatal("pause failed")
	}
	r = f.submit(t, "alice", map[string]any{"op": "resume", "taskId": id})
	ok(t, r)
	if r.Result.Task.Status != "active" || r.Result.Task.NextRun <= 0 {
		t.Fatal("resume failed")
	}
	r = f.submit(t, "alice", map[string]any{"op": "update", "taskId": id, "prompt": "Updated notes", "scheduleType": "interval", "scheduleValue": "1h"})
	ok(t, r)
	if r.Result.Task.Prompt != "Updated notes" || r.Result.Task.ScheduleValue != "1h" {
		t.Fatal("update failed")
	}
	r = f.submit(t, "alice", map[string]any{"op": "cancel", "taskId": id})
	ok(t, r)
	if r.Result.Task.Status != "done" {
		t.Fatal("cancel failed")
	}
	r = f.submit(t, "alice", map[string]any{"op": "delete", "taskId": id})
	ok(t, r)
	if !r.Result.Deleted {
		t.Fatal("delete failed")
	}
}

func TestTaskIPCRequestReplayUsesHostJournal(t *testing.T) {
	f := newWorkflow(t)
	created := f.submit(t, "alice", schedule("daily"))
	ok(t, created)
	id := created.Result.TaskID
	request := map[string]any{"op": "update", "taskId": id, "scheduleType": "interval", "scheduleValue": "1h"}
	first := f.named(t, "alice", "same-update", request)
	ok(t, first)
	time.Sleep(10 * time.Millisecond)
	second := f.named(t, "alice", "same-update", request)
	ok(t, second)
	if first.Result.Task.NextRun != second.Result.Task.NextRun {
		t.Fatal("replayed update shifted next run")
	}
	different := f.named(t, "alice", "same-update", map[string]any{"op": "delete", "taskId": id})
	if different.Status != "error" {
		t.Fatal("reused filename accepted different operation")
	}
	deleted := f.named(t, "alice", "same-delete", map[string]any{"op": "delete", "taskId": id})
	ok(t, deleted)
	replay := f.named(t, "alice", "same-delete", map[string]any{"op": "delete", "taskId": id})
	ok(t, replay)
	if !replay.Result.Deleted {
		t.Fatal("delete success lost on replay")
	}
}

func TestTaskIPCInvalidShapeReturnsActionableReceipt(t *testing.T) {
	f := newWorkflow(t)
	r := f.submit(t, "alice", map[string]any{"type": "schedule_task", "schedule_type": "cron", "schedule_value": "0 9 * * *"})
	if r.Status != "error" || !strings.Contains(r.Error, "expected op") {
		t.Fatalf("no actionable reason: %+v", r)
	}
	tasks, _ := f.st.ListTasksByChat(context.Background(), "alice")
	if len(tasks) != 0 {
		t.Fatal("invalid shape scheduled work")
	}
}

func TestTaskIPCFIFOAndSymlinkCannotBlockOrEscape(t *testing.T) {
	f := newWorkflow(t)
	tasks := filepath.Join(f.root, "ipc", "alice", "tasks")
	fifo := filepath.Join(tasks, "fifo.json")
	if err := syscall.Mkfifo(fifo, 0o600); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.json")
	if err := os.WriteFile(outside, []byte(`{"op":"schedule","prompt":"secret outside"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(tasks, "link.json")); err != nil {
		t.Fatal(err)
	}
	r := f.submit(t, "alice", schedule("healthy"))
	ok(t, r)
	if b, err := os.ReadFile(outside); err != nil || !strings.Contains(string(b), "secret outside") {
		t.Fatal("outside file changed", err)
	}
	f.stop() // shutdown remains responsive even after a FIFO request.
}

func TestTaskIPCReservedFIFOsDoNotBlockWatcher(t *testing.T) {
	for _, channel := range []string{".processed", ".failed", "results"} {
		t.Run(channel, func(t *testing.T) {
			f := newWorkflow(t)
			path := filepath.Join(f.root, "ipc", "alice", channel)
			if err := syscall.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
			// Alice's hostile channel must not block processing a different chat.
			r := f.submit(t, "bob", schedule("bob-daily"))
			ok(t, r)
			f.stop()
		})
	}
}

func TestTaskIPCReceiptSymlinkCannotWriteOrPruneOutside(t *testing.T) {
	f := newWorkflow(t)
	outside := t.TempDir()
	for i := 0; i < 3; i++ {
		if err := os.WriteFile(filepath.Join(outside, fmt.Sprintf("keep-%d.json", i)), []byte("outside"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(f.root, "ipc", "alice", "results")); err != nil {
		t.Fatal(err)
	}
	// A malicious source's results path cannot affect another chat's progress.
	r := f.submit(t, "bob", schedule("bob-safe"))
	ok(t, r)
	files, err := os.ReadDir(outside)
	if err != nil || len(files) != 3 {
		t.Fatalf("outside result directory changed: %v %v", files, err)
	}
}

func TestTaskIPCResponseFailureReplaysCommittedDelete(t *testing.T) {
	f := newWorkflow(t)
	created := f.submit(t, "alice", schedule("delete-me"))
	ok(t, created)
	id := created.Result.TaskID
	results := filepath.Join(f.root, "ipc", "alice", "results")
	saved := results + "-saved"
	if err := os.Rename(results, saved); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(results, []byte("block result writes"), 0o600); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(f.root, "ipc", "alice", "tasks", "delete-retry.json")
	if err := os.WriteFile(input, []byte(fmt.Sprintf(`{"op":"delete","taskId":%q}`, id)), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	committed := false
	for time.Now().Before(deadline) {
		if _, err := f.st.GetTask(context.Background(), id); err != nil {
			committed = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !committed {
		t.Fatal("delete did not commit while result writing failed")
	}
	if err := os.Remove(results); err != nil {
		t.Fatal(err)
	}
	// The watcher recreates results itself. Restoring the old directory races
	// with that mkdir and is not needed to replay the host journal response.
	result := filepath.Join(results, "delete-retry.result.json")
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if raw, err := os.ReadFile(result); err == nil {
			var r receipt
			if err = json.Unmarshal(raw, &r); err != nil {
				t.Fatal(err)
			}
			ok(t, r)
			if !r.Result.Deleted {
				t.Fatal("original delete result lost")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("committed delete could not replay its receipt")
}

func TestGroupTaskManagementDoesNotRevealForeignIDs(t *testing.T) {
	f := newWorkflow(t)
	if err := f.st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:-200", Folder: "group", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(f.root, "ipc", "group", "tasks"), 0o700); err != nil {
		t.Fatal(err)
	}
	created := f.submit(t, "owner", map[string]any{"op": "schedule", "taskId": "private-task-id", "prompt": "private-canary", "scheduleType": "interval", "scheduleValue": "1h"})
	if created.Status != "ok" {
		t.Fatalf("owner control failed: %+v", created)
	}
	for _, op := range []string{"get", "pause", "resume", "update", "delete", "cancel"} {
		known := f.submit(t, "group", map[string]any{"op": op, "taskId": "private-task-id"})
		missing := f.submit(t, "group", map[string]any{"op": op, "taskId": "missing-task-id"})
		if known.Status != "error" || missing.Status != "error" || known.Error != missing.Error || strings.Contains(known.Error, "private-task-id") {
			t.Fatalf("%s exposes foreign task: %+v %+v", op, known, missing)
		}
	}
	for _, id := range []string{"private-task-id", "missing-task-id"} {
		result := f.submit(t, "group", map[string]any{"op": "schedule", "taskId": id, "prompt": "group request", "scheduleType": "interval", "scheduleValue": "1h"})
		if result.Status != "error" || result.Error != "operation_unavailable" {
			t.Fatal("global custom task ID probe accepted")
		}
	}
	own := f.submit(t, "group", map[string]any{"op": "schedule", "prompt": "group request", "scheduleType": "interval", "scheduleValue": "1h"})
	if own.Status != "ok" || own.Result.TaskID == "" {
		t.Fatalf("host-assigned group schedule failed: %+v", own)
	}
	managed := f.submit(t, "group", map[string]any{"op": "get", "taskId": own.Result.TaskID})
	if managed.Status != "ok" {
		t.Fatalf("group cannot manage its returned ID: %+v", managed)
	}
}

func TestPrivateNonOwnerCannotChooseGlobalTaskID(t *testing.T) {
	f := newWorkflow(t)
	for _, id := range []string{"private-task", "missing-task", "preset-gmail"} {
		r := f.submit(t, "alice", map[string]any{"op": "schedule", "taskId": id, "prompt": "p", "scheduleType": "interval", "scheduleValue": "1h"})
		if r.Status != "error" || !strings.Contains(r.Error, "must omit taskId") {
			t.Fatalf("id probe: %+v", r)
		}
	}
	r := f.submit(t, "alice", schedule(""))
	ok(t, r)
	id := r.Result.TaskID
	bad := f.submit(t, "alice", map[string]any{"op": "update", "taskId": id, "scheduleType": "interval", "scheduleValue": "1ms"})
	if bad.Status != "error" {
		t.Fatal("update bypassed minimum period")
	}
	got, err := f.st.GetTask(t.Context(), id)
	if err != nil || got.ScheduleType != "cron" {
		t.Fatal("rejected update mutated task")
	}
}
