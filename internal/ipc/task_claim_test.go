package ipc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

// Real SQLite journal/transaction, without importing scheduler (which imports
// ipc). Existing external workflow tests cover the production scheduler adapter.
type claimTestSink struct {
	st     *store.Store
	ackErr error
}

func (s *claimTestSink) Schedule(ctx context.Context, task store.TaskRecord) error {
	_, err := s.st.CreateTask(ctx, task)
	return err
}
func (s *claimTestSink) Cancel(ctx context.Context, id string) error { return s.st.DeleteTask(ctx, id) }
func (s *claimTestSink) ProcessTaskRequest(ctx context.Context, id TaskRequestIdentity, apply func(context.Context) (TaskResult, error)) (TaskResult, error) {
	raw, err := s.st.ApplyTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention, func(ctx context.Context) ([]byte, error) {
		r, e := apply(ctx)
		if e != nil {
			return nil, e
		}
		return json.Marshal(r)
	})
	var conflict *store.TaskRequestConflict
	if errors.As(err, &conflict) {
		id.PayloadHash = conflict.OriginalHash
		return TaskResult{}, &TaskRequestConflict{Identity: id}
	}
	var r TaskResult
	if err == nil {
		err = json.Unmarshal(raw, &r)
	}
	return r, err
}
func (s *claimTestSink) AcknowledgeTaskRequest(ctx context.Context, id TaskRequestIdentity) error {
	if s.ackErr != nil {
		return s.ackErr
	}
	return s.st.AcknowledgeTaskRequest(ctx, id.SourceFolder, id.File, id.PayloadHash, id.Retention)
}

func claimHarness(t *testing.T) (*Watcher, *claimTestSink, string) {
	t.Helper()
	root := t.TempDir()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(t.Context(), filepath.Join(root, "store.sqlite"), log)
	if err != nil {
		t.Fatal(err)
	}

	if err = st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:2", Folder: "alice", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	sink := &claimTestSink{st: st}
	t.Cleanup(func() { sink.st.Close() })
	w := New(Config{DataDir: root, OwnerFolder: "owner"}, &fakeMessageSink{}, sink, log, staticResolver(map[string]string{"alice": "tg:2"}))
	writeIPC(t, root, "alice", "tasks", "replay.json", map[string]string{"op": "schedule", "prompt": "original", "scheduleType": "interval", "scheduleValue": "1h"})
	return w, sink, filepath.Join(root, "ipc", "alice")
}
func restarted(w *Watcher) *Watcher { return New(w.cfg, w.msgs, w.tasks, w.log, w.resolve) }
func claimReceipt(t *testing.T, base string) taskReceipt {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, "results", "replay.result.json"))
	if err != nil {
		t.Fatal(err)
	}
	var r taskReceipt
	if err = json.Unmarshal(raw, &r); err != nil {
		t.Fatal(err)
	}
	return r
}
func requireOneClaimTask(t *testing.T, s *claimTestSink) {
	t.Helper()
	tasks, err := s.st.ListTasksByChat(t.Context(), "alice")
	if err != nil || len(tasks) != 1 || tasks[0].Prompt != "original" {
		t.Fatalf("task mutation: %+v %v", tasks, err)
	}
}
func replaceClaimInput(t *testing.T, base string, raw []byte) {
	t.Helper()
	p := filepath.Join(base, "tasks", "replay.json")
	if err := os.WriteFile(p+".tmp", raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(p+".tmp", p); err != nil {
		t.Fatal(err)
	}
}

func TestTaskClaimReplacementAfterReceipt(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(fmt.Sprint(changed), func(t *testing.T) {
			w, sink, base := claimHarness(t)
			raw, err := os.ReadFile(filepath.Join(base, "tasks", "replay.json"))
			if err != nil {
				t.Fatal(err)
			}
			if changed {
				raw = []byte(`{"op":"schedule","prompt":"replacement","scheduleType":"interval","scheduleValue":"1h"}`)
			}
			injected := false
			w.taskClaimHook = func(stage string) error {
				if stage == "receipt" && !injected {
					injected = true
					if claimReceipt(t, base).Status != "ok" {
						t.Fatal("original failed")
					}
					if err := os.Remove(filepath.Join(base, "results", "replay.result.json")); err != nil {
						t.Fatal(err)
					}
					replaceClaimInput(t, base, raw)
				}
				return nil
			}
			w.tickOnce(t.Context())
			if !injected {
				t.Fatal("receipt boundary not reached")
			}
			if _, err := os.Stat(filepath.Join(base, "tasks", "replay.json")); err != nil {
				t.Fatal("replacement consumed by previous completion", err)
			}
			w = restarted(w)
			w.tickOnce(t.Context())
			got := claimReceipt(t, base)
			want := "ok"
			if changed {
				want = "error"
			}
			if got.Status != want {
				t.Fatalf("replacement receipt: %+v", got)
			}
			requireOneClaimTask(t, sink)
		})
	}
}

func TestTaskClaimRestartBoundaries(t *testing.T) {
	for _, stage := range []string{"prepared", "claimed", "snapshot", "dispatched", "outcome", "receipt", "terminal", "acknowledged"} {
		t.Run(stage, func(t *testing.T) {
			w, sink, base := claimHarness(t)
			hit := false
			w.taskClaimHook = func(s string) error {
				if s == stage {
					hit = true
					return errors.New("simulated process exit")
				}
				return nil
			}
			w.tickOnce(t.Context())
			if !hit {
				t.Fatal("boundary not reached")
			}
			if err := sink.st.Close(); err != nil {
				t.Fatal(err)
			}
			reopened, err := store.Open(t.Context(), filepath.Join(w.cfg.DataDir, "store.sqlite"), w.log)
			if err != nil {
				t.Fatal(err)
			}
			sink.st = reopened
			w = restarted(w)
			w.tickOnce(t.Context())
			if claimReceipt(t, base).Status != "ok" {
				t.Fatal("receipt not recovered")
			}
			requireOneClaimTask(t, sink)
			d, err := w.taskClaimDir("alice")
			if err != nil {
				t.Fatal(err)
			}
			entries, err := d.ReadDir(0)
			d.Close()
			if err != nil || len(entries) != 0 {
				t.Fatalf("claim cleanup: %v %v", entries, err)
			}
		})
	}
}

func TestTaskClaimSnapshotSurvivesOriginalDescriptorWrites(t *testing.T) {
	w, sink, base := claimHarness(t)
	f, err := os.OpenFile(filepath.Join(base, "tasks", "replay.json"), os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	w.taskClaimHook = func(stage string) error {
		if stage == "snapshot" {
			if err := f.Truncate(0); err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteAt([]byte(`{"op":"cancel","taskId":"victim"}`), 0); err != nil {
				t.Fatal(err)
			}
			return errors.New("exit")
		}
		return nil
	}
	w.tickOnce(t.Context())
	w = restarted(w)
	w.tickOnce(t.Context())
	requireOneClaimTask(t, sink)
}

func TestTaskClaimFailedReceiptRetainsPermanentOutcome(t *testing.T) {
	w, _, base := claimHarness(t)
	replaceClaimInput(t, base, []byte(`{"op":"unknown"}`))
	if err := os.WriteFile(filepath.Join(base, "results"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	w.tickOnce(t.Context())
	if err := os.Remove(filepath.Join(base, "results")); err != nil {
		t.Fatal(err)
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	if r := claimReceipt(t, base); r.Status != "error" || r.Error == "" {
		t.Fatalf("lost failure: %+v", r)
	}
}

func TestTaskClaimStaleNamespaceOrRegistrationHeld(t *testing.T) {
	for _, kind := range []string{"path", "inode", "jid", "owner"} {
		t.Run(kind, func(t *testing.T) {
			w, sink, base := claimHarness(t)
			w.taskClaimHook = func(stage string) error {
				if stage == "snapshot" {
					return errors.New("exit")
				}
				return nil
			}
			w.tickOnce(t.Context())
			w = restarted(w)
			switch kind {
			case "path":
				w.cfg.NamespacePath = func(string) (string, bool) { return base + "-new", true }
				if err := os.MkdirAll(base+"-new", 0700); err != nil {
					t.Fatal(err)
				}
			case "inode":
				if err := os.Rename(base, base+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(base, 0700); err != nil {
					t.Fatal(err)
				}
			case "jid":
				w.resolve = staticResolver(map[string]string{"alice": "tg:3"})
			case "owner":
				w.cfg.OwnerFolder = "alice"
			}
			w.tickOnce(t.Context())
			tasks, err := sink.st.ListTasksByChat(t.Context(), "alice")
			if err != nil || len(tasks) != 0 {
				t.Fatalf("stale claim dispatched: %v %v", tasks, err)
			}
		})
	}
}

func TestTaskClaimSpoolSymlinkAndPublicPermissionsRefused(t *testing.T) {
	for _, kind := range []string{"symlink", "public"} {
		t.Run(kind, func(t *testing.T) {
			w, sink, base := claimHarness(t)
			spool := filepath.Join(w.cfg.DataDir, "task-claims")
			if kind == "symlink" {
				if err := os.Symlink(t.TempDir(), spool); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Mkdir(spool, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(spool, 0777); err != nil {
					t.Fatal(err)
				}
			}
			w.tickOnce(t.Context())
			if _, err := os.Stat(filepath.Join(base, "tasks", "replay.json")); err != nil {
				t.Fatal("unsafe spool consumed input", err)
			}
			tasks, err := sink.st.ListTasksByChat(t.Context(), "alice")
			if err != nil || len(tasks) != 0 {
				t.Fatal("unsafe spool dispatched")
			}
		})
	}
}

func TestTaskClaimTerminalAckFailureDoesNotRepublishOrConsumeReplacement(t *testing.T) {
	w, sink, base := claimHarness(t)
	sink.ackErr = errors.New("ack unavailable")
	w.tickOnce(t.Context())
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("missing initial receipt")
	}
	if err := os.Remove(filepath.Join(base, "results", "replay.result.json")); err != nil {
		t.Fatal(err)
	}
	replaceClaimInput(t, base, []byte(`{"op":"schedule","prompt":"replacement","scheduleType":"interval","scheduleValue":"1h"}`))
	w = restarted(w)
	w.tickOnce(t.Context())
	if _, err := os.Stat(filepath.Join(base, "results", "replay.result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("old success republished for replacement")
	}
	sink.ackErr = nil
	w.tickOnce(t.Context())
	w.tickOnce(t.Context())
	if claimReceipt(t, base).Status != "error" {
		t.Fatal("conflict lost")
	}
	requireOneClaimTask(t, sink)
}

func TestTaskClaimStoreEpochChangeOnSameFilesystemHeld(t *testing.T) {
	w, sink, base := claimHarness(t)
	scope, err := sink.st.SnapshotScope(t.Context(), "alice")
	if err != nil {
		t.Fatal(err)
	}
	w.cfg.NamespaceScope = func(context.Context, string) (store.ScopeRef, error) { return scope, nil }
	w.taskClaimHook = func(stage string) error {
		if stage == "snapshot" {
			return errors.New("exit")
		}
		return nil
	}
	w.tickOnce(t.Context())
	scope.NamespaceEpoch++
	w = restarted(w)
	w.tickOnce(t.Context())
	if _, err := os.Stat(filepath.Join(base, "results", "replay.result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stale epoch published")
	}
	tasks, err := sink.st.ListTasksByChat(t.Context(), "alice")
	if err != nil || len(tasks) != 0 {
		t.Fatal("stale epoch dispatched", err)
	}
}

func TestTaskClaimRecoveryRotatesPastHeldClaims(t *testing.T) {
	w, sink, base := claimHarness(t)
	raw, err := os.ReadFile(filepath.Join(base, "tasks", "replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxChannelDispatches+5; i++ {
		name := fmt.Sprintf("held-%03d.json", i)
		if err := os.WriteFile(filepath.Join(base, "tasks", name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := w.openTaskClaim(t.Context(), "alice", name, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.snapshot(); err != nil {
			t.Fatal(err)
		}
		c.record.SourceJID = "tg:stale"
		if err = c.save(); err != nil {
			t.Fatal(err)
		}
		c.close()
	}
	c, err := w.openTaskClaim(t.Context(), "alice", "replay.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.snapshot(); err != nil {
		t.Fatal(err)
	}
	c.close()
	w = restarted(w)
	for range 5 {
		w.tickOnce(t.Context())
	}
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("healthy recovery starved")
	}
	requireOneClaimTask(t, sink)
}

func TestTaskClaimRecoveryPanicDoesNotKillSweep(t *testing.T) {
	w, sink, base := claimHarness(t)
	c, err := w.openTaskClaim(t.Context(), "alice", "replay.json", true)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.snapshot(); err != nil {
		t.Fatal(err)
	}
	c.close()
	// A panic in recovery must leave its exact claim intact and still permit
	// another folder's independent message to run in the same sweep.
	w.taskClaimHook = func(stage string) error {
		if stage == "dispatched" {
			panic("injected")
		}
		return nil
	}
	writeIPC(t, w.cfg.DataDir, "bob", "messages", "ok.json", map[string]string{"chatJid": "tg:3", "text": "healthy"})
	w.resolve = staticResolver(map[string]string{"alice": "tg:2", "bob": "tg:3"})
	w.tickOnce(t.Context())
	if len(w.msgs.(*fakeMessageSink).callsSnapshot()) != 1 {
		t.Fatal("panic killed sweep")
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	requireOneClaimTask(t, sink)
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("panic lost claim")
	}
}

func TestTaskClaimTerminalSurvivesJournalPruning(t *testing.T) {
	w, sink, base := claimHarness(t)
	w.cfg.Retention = 1
	w.taskClaimHook = func(stage string) error {
		if stage == "acknowledged" {
			return errors.New("exit")
		}
		return nil
	}
	w.tickOnce(t.Context())
	initial := claimReceipt(t, base)
	for _, name := range []string{"later-a", "later-b"} {
		if _, err := sink.st.ApplyTaskRequest(t.Context(), "alice", name, "hash", 1, func(context.Context) ([]byte, error) { return []byte(`{}`), nil }); err != nil {
			t.Fatal(err)
		}
		if err := sink.st.AcknowledgeTaskRequest(t.Context(), "alice", name, "hash", 1); err != nil {
			t.Fatal(err)
		}
	}
	// Remove the task to make any unintended replay observably recreate it.
	if err := sink.st.DeleteTask(t.Context(), initial.Result.TaskID); err != nil {
		t.Fatal(err)
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	tasks, err := sink.st.ListTasksByChat(t.Context(), "alice")
	if err != nil || len(tasks) != 0 {
		t.Fatal("terminal claim replayed after journal pruning", err)
	}
}

func TestTaskClaimArchiveFailureRetainsOutcome(t *testing.T) {
	w, sink, base := claimHarness(t)
	w.taskClaimHook = func(stage string) error {
		if stage == "outcome" {
			return errors.New("exit")
		}
		return nil
	}
	w.tickOnce(t.Context())
	archive := filepath.Join(base, ".processed")
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archive, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	requireOneClaimTask(t, sink)
	if _, err := os.Stat(filepath.Join(base, "results", "replay.result.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("archive failure claimed complete")
	}
	if err := os.Remove(archive); err != nil {
		t.Fatal(err)
	}
	w.tickOnce(t.Context())
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("archive recovery failed")
	}
	requireOneClaimTask(t, sink)
}

func TestTaskClaimCapacityPreservesPendingInputs(t *testing.T) {
	w, sink, base := claimHarness(t)
	w.cfg.Retention = 1
	w.taskClaimHook = func(stage string) error {
		if stage == "snapshot" {
			return errors.New("exit")
		}
		return nil
	}
	w.tickOnce(t.Context())
	path := filepath.Join(base, "tasks", "new.json")
	if err := os.WriteFile(path, []byte(`{"op":"schedule","prompt":"new","scheduleType":"interval","scheduleValue":"1h"}`), 0600); err != nil {
		t.Fatal(err)
	}
	// Recovery is held by an explicit binding mismatch. Capacity must refuse new
	// work without deleting the unresolved claim or consuming the new inbox entry.
	c, err := w.openTaskClaim(t.Context(), "alice", "replay.json", false)
	if err != nil {
		t.Fatal(err)
	}
	c.record.SourceJID = "stale"
	if err = c.save(); err != nil {
		t.Fatal(err)
	}
	c.close()
	w = restarted(w)
	w.tickOnce(t.Context())
	if _, err := os.Stat(path); err != nil {
		t.Fatal("full spool consumed pending input", err)
	}
	tasks, err := sink.st.ListTasksByChat(t.Context(), "alice")
	if err != nil || len(tasks) != 0 {
		t.Fatal("capacity bypassed", err)
	}
}

func TestTaskClaimTransientReadFailurePreservesOriginal(t *testing.T) {
	w, sink, base := claimHarness(t)
	w.taskClaimHook = func(stage string) error {
		if stage == "read" {
			return errors.New("transient I/O failure")
		}
		return nil
	}
	w.tickOnce(t.Context())
	c, err := w.openTaskClaim(t.Context(), "alice", "replay.json", false)
	if err != nil {
		t.Fatal(err)
	}
	if c.record.Snapshotted || c.record.ReadError != "" {
		t.Fatal("transient read became terminal rejection")
	}
	raw, err := readClaimFile(c.dir, c.slot+".input", 64<<10)
	c.close()
	if err != nil || len(raw) == 0 {
		t.Fatal("original lost", err)
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	requireOneClaimTask(t, sink)
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("transient read not recovered")
	}
}

func TestTaskClaimHeldReplacementsDoNotStarveNewAdmission(t *testing.T) {
	w, _, base := claimHarness(t)
	raw, err := os.ReadFile(filepath.Join(base, "tasks", "replay.json"))
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxChannelDispatches+2; i++ {
		name := fmt.Sprintf("a-held-%03d.json", i)
		if err = os.WriteFile(filepath.Join(base, "tasks", name), raw, 0600); err != nil {
			t.Fatal(err)
		}
		c, err := w.openTaskClaim(t.Context(), "alice", name, true)
		if err != nil {
			t.Fatal(err)
		}
		if err = c.snapshot(); err != nil {
			t.Fatal(err)
		}
		c.record.SourceJID = "stale"
		if err = c.save(); err != nil {
			t.Fatal(err)
		}
		c.close()
		if err = os.WriteFile(filepath.Join(base, "tasks", name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	w = restarted(w)
	w.tickOnce(t.Context())
	if claimReceipt(t, base).Status != "ok" {
		t.Fatal("new admission starved behind held replacements")
	}
}
