package ipc

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type durableFixture struct {
	t     *testing.T
	st    *store.Store
	root  string
	clk   *fakeClock
	sink  *fakeMessageSink
	tasks *fakeTaskSink
	jids  map[string]string
	store DurableStore
	jid   string
}

func newDurableFixture(t *testing.T) *durableFixture {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, c := range []store.RegisteredChat{
		{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1},
		{JID: "tg:-2", Folder: "group-a", AddedAt: 2},
	} {
		if err := st.RegisterChat(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	return &durableFixture{t: t, st: st, root: t.TempDir(), clk: &fakeClock{now: time.Unix(0, 0)},
		sink: &fakeMessageSink{}, tasks: &fakeTaskSink{}, store: st, jid: "tg:1",
		jids: map[string]string{"owner": "tg:1", "group-a": "tg:-2"}}
}

// watcher builds a fresh Watcher over the same data: a new process.
func (f *durableFixture) watcher() *Watcher {
	return New(Config{DataDir: f.root, OwnerFolder: "owner", NowFn: f.clk.Now,
		Durable: DurableConfig{Store: f.store, PersonalJID: f.jid}},
		f.sink, f.tasks, slog.New(slog.NewTextHandler(io.Discard, nil)), staticResolver(f.jids))
}

func (f *durableFixture) send(folder, name, jid, text string) {
	f.t.Helper()
	writeIPC(f.t, f.root, folder, "messages", name, map[string]string{"chatJid": jid, "text": text})
}

func (f *durableFixture) dir(folder, sub string) string {
	return filepath.Join(f.root, "ipc", folder, sub)
}

func (f *durableFixture) receipt(folder, name string) map[string]any {
	f.t.Helper()
	b, err := os.ReadFile(filepath.Join(f.dir(folder, "results/messages"), name))
	if err != nil {
		f.t.Fatalf("receipt %s: %v", name, err)
	}
	var m map[string]any
	if err = json.Unmarshal(b, &m); err != nil {
		f.t.Fatal(err)
	}
	return m
}

func (f *durableFixture) count(query string) int {
	f.t.Helper()
	var n int
	if err := f.st.DB().QueryRowContext(context.Background(), query).Scan(&n); err != nil {
		f.t.Fatal(err)
	}
	return n
}

func (f *durableFixture) deliveries() int { return f.count(`SELECT count(*) FROM delivery_groups`) }

func (f *durableFixture) setState(state string, unresolved int) {
	f.t.Helper()
	if _, err := f.st.DB().ExecContext(context.Background(), `UPDATE delivery_groups SET state=?, unresolved=?`, state, unresolved); err != nil {
		f.t.Fatal(err)
	}
}

func (f *durableFixture) tick(w *Watcher) { w.tickOnce(context.Background()) }

func TestDurable_QueuedReceiptArchiveAndNoDirectSend(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())

	if calls := f.sink.callsSnapshot(); len(calls) != 0 {
		t.Fatalf("durable request used the direct sink: %+v", calls)
	}
	if f.deliveries() != 1 {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
	var thread, parts int
	var owner, vis string
	if err := f.st.DB().QueryRow(`SELECT thread_id,visibility,delivery_owner,part_count FROM delivery_groups`).Scan(&thread, &vis, &owner, &parts); err != nil {
		t.Fatal(err)
	}
	if thread != 0 || vis != "conversation" || owner != string(store.OwnerLegacyAgentIPC) || parts != 1 {
		t.Fatalf("plan: thread=%d vis=%s owner=%s parts=%d", thread, vis, owner, parts)
	}
	rc := f.receipt("owner", "0001-a.result.json")
	if rc["status"] != "queued" || rc["deliveryState"] != "pending" {
		t.Fatalf("receipt: %v", rc)
	}
	if got := dirEntries(t, f.dir("owner", ".processed")); len(got) != 1 {
		t.Fatalf(".processed = %v", got)
	}
	if got := dirEntries(t, f.dir("owner", "messages")); len(got) != 0 {
		t.Fatalf("messages = %v", got)
	}
}

func TestDurable_ReceiptFollowsDeliveryState(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	w := f.watcher()
	f.tick(w)
	for _, c := range []struct {
		state      string
		unresolved int
		want       string
	}{
		{"sending", 1, "queued"}, {"sent", 0, "sent"}, {"uncertain", 1, "uncertain"}, {"failed", 0, "failed"}, {"expired", 0, "failed"},
	} {
		f.setState(c.state, c.unresolved)
		f.tick(w)
		rc := f.receipt("owner", "0001-a.result.json")
		if rc["status"] != c.want || rc["deliveryState"] != c.state {
			t.Fatalf("state %s: receipt %v", c.state, rc)
		}
	}
}

func TestDurable_RestartAfterCommitBeforeReceipt(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	// The previous process committed the delivery and died before the receipt.
	path := filepath.Join(f.dir("owner", "messages"), "0001-a.json")
	raw, _ := os.ReadFile(path)
	w := f.watcher()
	conv, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 0, store.VisibilityConversation)
	id := requestIdentity(path, raw)
	if _, err := f.st.PublishDelivery(t.Context(), store.PublishRequest{Conversation: conv, Owner: store.OwnerLegacyAgentIPC,
		ActorRef: "ipc:" + conv.Scope.NamespaceID, Parts: splitText("hello"), Request: &id}); err != nil {
		t.Fatal(err)
	}
	f.tick(w)
	if f.deliveries() != 1 {
		t.Fatalf("restart published again: %d deliveries", f.deliveries())
	}
	if rc := f.receipt("owner", "0001-a.result.json"); rc["status"] != "queued" || rc["duplicate"] != true {
		t.Fatalf("receipt: %v", rc)
	}
	if len(dirEntries(t, f.dir("owner", ".processed"))) != 1 {
		t.Fatal("not archived")
	}
}

func TestDurable_FailedArchiveDoesNotRepublishAfterRestart(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	blocker := f.dir("owner", ".processed")
	os.RemoveAll(blocker)
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.tick(f.watcher())
	if f.deliveries() != 1 || len(dirEntries(t, f.dir("owner", "messages"))) != 1 {
		t.Fatalf("expected committed delivery and a file left behind: %d %v", f.deliveries(), dirEntries(t, f.dir("owner", "messages")))
	}
	if f.receipt("owner", "0001-a.result.json")["status"] != "queued" {
		t.Fatal("receipt must exist before archive")
	}
	os.Remove(blocker)
	f.tick(f.watcher()) // restart
	if f.deliveries() != 1 {
		t.Fatalf("second publication after failed archive: %d", f.deliveries())
	}
	if len(dirEntries(t, f.dir("owner", ".processed"))) != 1 || len(dirEntries(t, f.dir("owner", "messages"))) != 0 {
		t.Fatal("not archived after restart")
	}
}

func TestDurable_FailedReceiptRetriesWithoutRepublishing(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	blocker := f.dir("owner", "results")
	os.MkdirAll(f.dir("owner", ".processed"), 0o700)
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.tick(f.watcher())
	if f.deliveries() != 1 || len(dirEntries(t, f.dir("owner", "messages"))) != 1 {
		t.Fatal("expected committed delivery with the request kept")
	}
	os.Remove(blocker)
	f.tick(f.watcher())
	if f.deliveries() != 1 || len(dirEntries(t, f.dir("owner", ".processed"))) != 1 {
		t.Fatalf("deliveries=%d", f.deliveries())
	}
}

func TestDurable_ReceiptRecreatedFromDurableStateAfterCrash(t *testing.T) {
	f := newDurableFixture(t)
	if err := os.MkdirAll(f.dir("owner", "messages"), 0o700); err != nil {
		t.Fatal(err)
	}
	conv, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 0, store.VisibilityConversation)
	id := store.RequestIdentity{Key: "0009-z.json", PayloadHash: strings.Repeat("a", 64)}
	if _, err := f.st.PublishDelivery(t.Context(), store.PublishRequest{Conversation: conv, Owner: store.OwnerLegacyAgentIPC, Parts: splitText("hi"), Request: &id}); err != nil {
		t.Fatal(err)
	}
	f.setState("sent", 0)
	f.tick(f.watcher())
	if rc := f.receipt("owner", "0009-z.result.json"); rc["status"] != "sent" {
		t.Fatalf("receipt: %v", rc)
	}
}

func TestDurable_RepeatedFilenameSameBytesNoSecondDelivery(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())
	if f.deliveries() != 1 {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
	if rc := f.receipt("owner", "0001-a.result.json"); rc["status"] != "queued" || rc["duplicate"] != true {
		t.Fatalf("receipt: %v", rc)
	}
}

func TestDurable_ConflictingBytesRejectedAndOriginalReceiptKept(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())
	f.send("owner", "0001-a.json", "tg:1", "different")
	f.tick(f.watcher())
	if f.deliveries() != 1 {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
	if rc := f.receipt("owner", "0001-a.result.json"); rc["status"] != "queued" {
		t.Fatalf("original receipt was overwritten: %v", rc)
	}
	rc := f.receipt("owner", "0001-a.conflict.result.json")
	if rc["status"] != "error" || rc["error"] != codeRequestConflict {
		t.Fatalf("conflict receipt: %v", rc)
	}
	if len(dirEntries(t, f.dir("owner", ".failed"))) != 1 {
		t.Fatal("conflicting file not moved to .failed")
	}
}

func TestDurable_UnavailableRecipientRejectedAndTombstoned(t *testing.T) {
	f := newDurableFixture(t)
	f.jid, f.jids["owner"] = "tg:5", "tg:5" // owner bound to a chat that is not registered
	f.send("owner", "0001-a.json", "tg:5", "hello")
	w := f.watcher()
	f.tick(w)
	if len(f.sink.callsSnapshot()) != 0 || f.deliveries() != 0 {
		t.Fatal("rejected request must neither send directly nor publish")
	}
	rc := f.receipt("owner", "0001-a.result.json")
	if rc["status"] != "error" || rc["error"] != codeRecipientUnavailable {
		t.Fatalf("receipt: %v", rc)
	}
	if n := f.count(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`); n != 1 {
		t.Fatalf("tombstones = %d", n)
	}
	if len(dirEntries(t, f.dir("owner", ".failed"))) != 1 {
		t.Fatal("not moved to .failed")
	}
	// Resubmitting the same bytes stays rejected, still without a send.
	f.send("owner", "0001-a.json", "tg:5", "hello")
	f.tick(f.watcher())
	if len(f.sink.callsSnapshot()) != 0 || f.deliveries() != 0 || f.count(`SELECT count(*) FROM ipc_message_requests`) != 1 {
		t.Fatal("replayed rejection changed state")
	}
}

func TestDurable_GroupAndSharedChatsKeepLegacyPath(t *testing.T) {
	f := newDurableFixture(t)
	f.send("group-a", "0001-g.json", "tg:-2", "from group")   // group source to its own chat
	f.send("owner", "0002-o.json", "tg:-2", "owner to group") // owner to another chat
	f.send("group-a", "0003-x.json", "tg:1", "cross chat")    // denied by legacy authz
	f.tick(f.watcher())
	calls := f.sink.callsSnapshot()
	if len(calls) != 2 || calls[0].text != "from group" || calls[1].text != "owner to group" {
		t.Fatalf("legacy sends: %+v", calls)
	}
	if f.deliveries() != 0 || f.count(`SELECT count(*) FROM ipc_message_requests`) != 0 {
		t.Fatal("non-personal request reached the durable store")
	}
	if rc := f.receipt("group-a", "0003-x.result.json"); rc["status"] != "error" {
		t.Fatalf("legacy denial changed: %v", rc)
	}
}

func TestDurable_DisabledKeepsLegacyForOwner(t *testing.T) {
	f := newDurableFixture(t)
	f.store = nil
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())
	if calls := f.sink.callsSnapshot(); len(calls) != 1 || f.deliveries() != 0 {
		t.Fatalf("calls=%+v deliveries=%d", calls, f.deliveries())
	}
}

func TestDurable_TaskIPCHasOneDeliveryOwner(t *testing.T) {
	f := newDurableFixture(t)
	writeIPC(t, f.root, "owner", "tasks", "0001-t.json", map[string]any{
		"op": "schedule", "taskId": "t1", "prompt": "report", "scheduleType": "interval", "scheduleValue": "1h",
	})
	f.tick(f.watcher())
	if len(f.tasks.scheduled) != 1 {
		t.Fatalf("scheduled = %d", len(f.tasks.scheduled))
	}
	if f.deliveries() != 0 || len(f.sink.callsSnapshot()) != 0 {
		t.Fatal("a task request must not create any delivery")
	}
}

func TestDurable_StaleScopeBetweenSnapshotAndPublishRetries(t *testing.T) {
	f := newDurableFixture(t)
	f.store = &staleOnce{Store: f.st, st: f.st}
	f.send("owner", "0001-a.json", "tg:1", "hello")
	w := f.watcher()
	f.tick(w)
	if f.deliveries() != 0 || len(dirEntries(t, f.dir("owner", "messages"))) != 1 {
		t.Fatal("stale scope must not publish and must keep the request")
	}
	if rc := f.receipt("owner", "0001-a.result.json"); rc["status"] != "retrying" {
		t.Fatalf("receipt: %v", rc)
	}
	f.tick(w) // inside the backoff window: skipped
	if f.deliveries() != 0 {
		t.Fatal("retried inside backoff")
	}
	f.clk.advance(time.Minute)
	f.tick(w)
	if f.deliveries() != 1 || f.receipt("owner", "0001-a.result.json")["status"] != "queued" {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
}

// staleOnce bumps the registration revision after the first conversation
// snapshot, so the first publication sees a stale ScopeRef.
type staleOnce struct {
	*store.Store
	st   *store.Store
	done bool
}

func (s *staleOnce) SnapshotConversation(ctx context.Context, jid string, thread int, v store.Visibility) (store.ConversationScope, error) {
	conv, err := s.Store.SnapshotConversation(ctx, jid, thread, v)
	if err == nil && !s.done {
		s.done = true
		err = s.st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"})
	}
	return conv, err
}

func TestDurable_QuotaDefersThenAdmits(t *testing.T) {
	f := newDurableFixture(t)
	if err := f.st.SetOutboxLimits(store.OutboxLimits{MaxUnresolvedPerNamespace: 1}); err != nil {
		t.Fatal(err)
	}
	f.send("owner", "0001-a.json", "tg:1", "one")
	f.send("owner", "0002-b.json", "tg:1", "two")
	w := f.watcher()
	f.tick(w)
	if f.deliveries() != 1 {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
	if rc := f.receipt("owner", "0002-b.result.json"); rc["status"] != "retrying" || rc["error"] != codeQuotaExceeded {
		t.Fatalf("receipt: %v", rc)
	}
	f.setState("sent", 0)
	f.clk.advance(time.Minute)
	f.tick(w)
	if f.deliveries() != 2 || f.receipt("owner", "0002-b.result.json")["status"] != "queued" {
		t.Fatalf("deliveries = %d", f.deliveries())
	}
}

func TestDurable_ExpiredAndEmptyRequestsRejected(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-e.json", "tg:1", "   ")
	f.send("owner", "0002-o.json", "tg:1", "old")
	f.clk.advance(2 * store.DefaultDeliveryExpiry)
	f.clk.now = time.Now().Add(2 * store.DefaultDeliveryExpiry)
	f.tick(f.watcher())
	if f.deliveries() != 0 {
		t.Fatal("published")
	}
	if rc := f.receipt("owner", "0001-e.result.json"); rc["error"] != codeEmptyText {
		t.Fatalf("empty: %v", rc)
	}
	if rc := f.receipt("owner", "0002-o.result.json"); rc["error"] != codeRequestExpired {
		t.Fatalf("expired: %v", rc)
	}
	if n := f.count(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`); n != 2 {
		t.Fatalf("tombstones = %d", n)
	}
}

func TestDurable_LongTextSplitsUTF8Safe(t *testing.T) {
	text := strings.Repeat("я", 5000) // 10000 bytes
	parts := splitText(text)
	var joined strings.Builder
	for _, p := range parts {
		if len(p.Payload) > textPartBytes {
			t.Fatalf("part of %d bytes", len(p.Payload))
		}
		joined.Write(p.Payload)
	}
	if joined.String() != text || len(parts) < 3 {
		t.Fatalf("parts=%d lossless=%v", len(parts), joined.String() == text)
	}
}

func TestLegacyDeliveryFailureStaysInIPCAndTouchesNoTasks(t *testing.T) {
	f := newDurableFixture(t)
	f.store = nil
	f.sink.responses = []error{ErrPermanent}
	f.send("owner", "0001-a.json", "tg:1", "hello")
	f.tick(f.watcher())
	if len(f.tasks.scheduled)+len(f.tasks.cancelled) != 0 || f.deliveries() != 0 {
		t.Fatal("legacy failure must not reach task or outbox state")
	}
	if len(dirEntries(t, f.dir("owner", ".failed"))) != 1 {
		t.Fatal("failed request not archived")
	}
}

func TestDurable_LingeringCommittedFileConvergesInsteadOfExpiring(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	path := filepath.Join(f.dir("owner", "messages"), "0001-a.json")
	raw, _ := os.ReadFile(path)
	conv, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 0, store.VisibilityConversation)
	id := requestIdentity(path, raw)
	if _, err := f.st.PublishDelivery(t.Context(), store.PublishRequest{Conversation: conv, Owner: store.OwnerLegacyAgentIPC,
		ActorRef: "ipc:" + conv.Scope.NamespaceID, Parts: splitText("hello"), Request: &id}); err != nil {
		t.Fatal(err)
	}
	// Crash before the receipt; the file then lingers past the delivery lifetime.
	f.clk.now = time.Now()
	old := f.clk.now.Add(-2 * store.DefaultDeliveryExpiry)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	f.tick(f.watcher())
	if f.deliveries() != 1 || f.count(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`) != 0 {
		t.Fatal("committed request was rejected or republished")
	}
	if rc := f.receipt("owner", "0001-a.result.json"); rc["status"] != "queued" || rc["error"] != nil {
		t.Fatalf("receipt: %v", rc)
	}
	if len(dirEntries(t, f.dir("owner", ".processed"))) != 1 {
		t.Fatal("not archived as processed")
	}
}

func TestDurable_TerminalWriteFailuresAreThrottledWithoutTombstone(t *testing.T) {
	f := newDurableFixture(t)
	f.send("owner", "0001-a.json", "tg:1", "hello")
	blocker := f.dir("owner", "results")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := f.watcher()
	for i := 0; i < 8; i++ { // far past the retry budget
		f.tick(w)
		f.clk.advance(6 * time.Second)
	}
	// Unblock; a poll inside the cooldown must not retry.
	os.Remove(blocker)
	f.tick(w)
	if len(dirEntries(t, f.dir("owner", ".processed"))) != 0 {
		t.Fatal("retried inside the cooldown")
	}
	f.clk.advance(time.Minute)
	f.tick(w)
	if f.deliveries() != 1 || len(dirEntries(t, f.dir("owner", ".processed"))) != 1 {
		t.Fatalf("deliveries=%d", f.deliveries())
	}
	if f.count(`SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`) != 0 {
		t.Fatal("a rejection tombstone was written")
	}
}
