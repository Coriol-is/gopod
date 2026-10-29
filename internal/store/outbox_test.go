package store

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPublishSameHashSameDeliveryChangedHashConflicts(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	res := f.freeze(ExecInteractive, "answer", ResultMember{TurnID: turn, Attempt: 1})

	first, err := f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "a", "b"))
	if err != nil || !first.Created || first.State != DeliveryPending || !first.Unresolved || len(first.Parts) != 2 || first.Sequence != 1 {
		t.Fatalf("first: %+v %v", first, err)
	}
	if first.Parts[0].Ordinal != 0 || first.Parts[1].Ordinal != 1 || string(first.Parts[1].Part.Payload) != "b" {
		t.Fatalf("parts out of order: %+v", first.Parts)
	}
	same, err := f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "a", "b"))
	if err != nil || same.ID != first.ID || same.Created {
		t.Fatalf("same plan did not return the same delivery: %+v %v", same, err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "a", "changed")); !errors.Is(err, ErrPlanConflict) {
		t.Fatalf("changed plan: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 1 || countRows(t, f.st, `SELECT count(*) FROM outbox_parts`) != 2 {
		t.Fatal("duplicate publication left rows")
	}
}

func TestIPCRequestIdentity(t *testing.T) {
	f := newDeliveryFixture(t)
	hash := sha256Hex([]byte("bytes"))
	mk := func(key, h string, texts ...string) PublishRequest {
		r := f.publishReq("", texts...)
		r.Owner = OwnerLegacyAgentIPC
		r.Request = &RequestIdentity{Key: key, PayloadHash: h}
		return r
	}
	first, err := f.st.PublishDelivery(t.Context(), mk("m1.json", hash, "hi"))
	if err != nil {
		t.Fatal(err)
	}
	same, err := f.st.PublishDelivery(t.Context(), mk("m1.json", hash, "hi"))
	if err != nil || same.ID != first.ID || same.Created {
		t.Fatalf("replay: %+v %v", same, err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), mk("m1.json", sha256Hex([]byte("other")), "hi")); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("changed bytes: %v", err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), mk("m1.json", hash, "different plan")); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("same bytes, different plan: %v", err)
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 1 {
		t.Fatal("conflict created a delivery")
	}

	rejHash := sha256Hex([]byte("bad"))
	if ok, err := f.st.RecordRequestRejection(t.Context(), f.conv.Scope, RequestIdentity{Key: "bad.json", PayloadHash: rejHash}, "unauthorized_recipient"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, err := f.st.RecordRequestRejection(t.Context(), f.conv.Scope, RequestIdentity{Key: "bad.json", PayloadHash: rejHash}, "unauthorized_recipient"); err != nil || !ok {
		t.Fatalf("idempotent rejection: %v", err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), mk("bad.json", rejHash, "x")); !errors.Is(err, ErrRequestRejected) {
		t.Fatalf("rejected request published: %v", err)
	}
	if _, err = f.st.RecordRequestRejection(t.Context(), f.conv.Scope, RequestIdentity{Key: "bad.json", PayloadHash: hash}, "x"); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("rejection reuse: %v", err)
	}
	if _, err = f.st.db.ExecContext(t.Context(), `UPDATE ipc_message_requests SET payload_hash='0'`); err == nil {
		t.Fatal("tombstone rewritten")
	}
}

func TestRequestKeyIsScopedByNamespace(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 2}); err != nil {
		t.Fatal(err)
	}
	other, _ := f.st.SnapshotConversation(t.Context(), "tg:-2", 0, VisibilityConversation)
	id := &RequestIdentity{Key: "same.json", PayloadHash: sha256Hex([]byte("x"))}
	a, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: textParts("a"), Request: id})
	if err != nil {
		t.Fatal(err)
	}
	b, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: other, Owner: OwnerLegacyAgentIPC, Parts: textParts("a"), Request: id})
	if err != nil || a.ID == b.ID {
		t.Fatalf("namespaces shared a request key: %+v %v", b, err)
	}
}

func TestSequencePerConversation(t *testing.T) {
	f := newDeliveryFixture(t)
	pub := func(c ConversationScope) int64 {
		r := f.publishReq("", "x")
		r.Conversation, r.Owner = c, OwnerLegacyAgentIPC
		d, err := f.st.PublishDelivery(t.Context(), r)
		if err != nil {
			t.Fatal(err)
		}
		return d.Sequence
	}
	topic, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 7, VisibilityConversation)
	private, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 0, VisibilityPrivate)
	got := []int64{pub(f.conv), pub(f.conv), pub(topic), pub(private), pub(f.conv), pub(topic)}
	want := []int64{1, 2, 1, 1, 3, 2}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sequences %v, want %v", got, want)
		}
	}
}

func TestStaleScopeAlwaysRejectedAfterRegistrationChange(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		r := f.publishReq("", fmt.Sprintf("m%d", i))
		r.Owner = OwnerLegacyAgentIPC
		if _, err := f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrStaleScope) {
			t.Fatalf("publish %d with the old ScopeRef: %v", i, err)
		}
	}
	if countRows(t, f.st, `SELECT count(*) FROM delivery_groups`) != 0 || countRows(t, f.st, `SELECT count(*) FROM conversation_sequences`) != 0 {
		t.Fatal("stale publication left rows")
	}
}

// Validation must happen inside the publishing transaction. A trigger records,
// at insert time and in the same transaction, the revision being stored next to
// the live registration revision; any gap means validation raced a registration
// change.
func TestPublishRevisionMatchesRegistrationAtCommit(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := context.Background()
	for _, stmt := range []string{
		`CREATE TABLE audit (stored INTEGER, live INTEGER)`,
		`CREATE TRIGGER audit_publish BEFORE INSERT ON delivery_groups BEGIN
 INSERT INTO audit SELECT NEW.registration_revision, registration_revision FROM registered_chats WHERE jid=NEW.chat_jid; END`,
	} {
		if _, err := f.st.db.ExecContext(ctx, stmt); err != nil {
			t.Fatal(err)
		}
	}
	var accepted, stale atomic.Int64
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 40; i++ {
				conv, err := f.st.SnapshotConversation(ctx, "tg:1", 0, VisibilityConversation)
				if err != nil {
					t.Error(err)
					return
				}
				r := PublishRequest{Conversation: conv, Owner: OwnerLegacyAgentIPC, Parts: textParts("x")}
				switch _, err = f.st.PublishDelivery(ctx, r); {
				case err == nil:
					accepted.Add(1)
				case errors.Is(err, ErrStaleScope):
					stale.Add(1)
				default:
					t.Error(err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 60; i++ {
			if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: fmt.Sprintf("n%d", i)}); err != nil {
				t.Error(err)
			}
		}
	}()
	wg.Wait()
	if accepted.Load() == 0 {
		t.Fatal("no publication was accepted; the check proves nothing")
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM audit`); int64(n) != accepted.Load() {
		t.Fatalf("audit rows %d, accepted %d", n, accepted.Load())
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM audit WHERE stored<>live`); n != 0 {
		t.Fatalf("%d deliveries stored under a revision that was not current at commit", n)
	}
	t.Logf("accepted=%d stale=%d", accepted.Load(), stale.Load())
}

func TestRetiredNamespaceRejectsPublication(t *testing.T) {
	f := newDeliveryFixture(t)
	if _, err := f.st.db.ExecContext(t.Context(), `DELETE FROM registered_chats WHERE jid='tg:1'`); err != nil {
		t.Fatal(err)
	}
	r := f.publishReq("", "x")
	r.Owner = OwnerLegacyAgentIPC
	if _, err := f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("retired namespace published: %v", err)
	}
}

func TestPublishRollbackLeavesNoPartialGroup(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	if _, err := f.st.db.ExecContext(ctx, `CREATE TRIGGER fail_third BEFORE INSERT ON outbox_parts WHEN NEW.ordinal=2 BEGIN SELECT RAISE(ABORT,'boom'); END`); err != nil {
		t.Fatal(err)
	}
	req := f.publishReq("", "a", "b", "c")
	req.Owner = OwnerLegacyAgentIPC
	req.Request = &RequestIdentity{Key: "r.json", PayloadHash: sha256Hex([]byte("r"))}
	assertEmpty := func(label string) {
		t.Helper()
		for _, table := range []string{"delivery_groups", "outbox_parts", "ipc_message_requests", "conversation_sequences"} {
			if n := countRows(t, f.st, "SELECT count(*) FROM "+table); n != 0 {
				t.Fatalf("%s: %s kept %d rows", label, table, n)
			}
		}
	}
	notified := 0
	f.st.SetPublishNotifier(func(string) { notified++ })
	if _, err := f.st.PublishDelivery(ctx, req); err == nil {
		t.Fatal("expected failure")
	}
	assertEmpty("standalone")

	// Enlisted in a caller transaction that commits anyway: the savepoint must undo the group.
	_, err := f.st.ApplyTaskRequest(ctx, "owner", "req.json", "h", 5, func(inner context.Context) ([]byte, error) {
		if _, e := f.st.PublishDelivery(inner, req); e == nil {
			t.Error("expected enlisted failure")
		}
		return []byte("ok"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	assertEmpty("enlisted")
	if notified != 0 {
		t.Fatal("notified for a failed publication")
	}
}

func TestEnlistedPublishCommitsWithOuterAndDefersNotification(t *testing.T) {
	f := newDeliveryFixture(t)
	notified := 0
	f.st.SetPublishNotifier(func(string) { notified++ })
	var id string
	_, err := f.st.ApplyTaskRequest(t.Context(), "owner", "req.json", "h", 5, func(inner context.Context) ([]byte, error) {
		r := f.publishReq("", "x")
		r.Owner = OwnerLegacyAgentIPC
		d, e := f.st.PublishDelivery(inner, r)
		id = d.ID
		return []byte("ok"), e
	})
	if err != nil {
		t.Fatal(err)
	}
	if notified != 0 {
		t.Fatal("notified before the outer transaction committed")
	}
	if _, err = f.st.GetDelivery(t.Context(), id); err != nil {
		t.Fatalf("enlisted delivery not durable: %v", err)
	}
	f.st.NotifyPublished(id)
	if notified != 1 {
		t.Fatal("explicit notification lost")
	}
}

func TestNotificationOnlyAfterCommit(t *testing.T) {
	f := newDeliveryFixture(t)
	var seen []string
	f.st.SetPublishNotifier(func(id string) {
		// The transaction is closed here, so the group is already readable.
		if _, err := f.st.GetDelivery(context.Background(), id); err != nil {
			t.Errorf("notified before commit: %v", err)
		}
		seen = append(seen, id)
	})
	r := f.publishReq("", "x")
	r.Owner = OwnerLegacyAgentIPC
	r.Request = &RequestIdentity{Key: "n.json", PayloadHash: sha256Hex([]byte("n"))}
	d, err := f.st.PublishDelivery(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != d.ID {
		t.Fatalf("notifications %v", seen)
	}
}

func TestPublishValidation(t *testing.T) {
	f := newDeliveryFixture(t)
	turn := f.turn("telegram", "1:1", 0, 1)
	res := f.freeze(ExecInteractive, "a", ResultMember{TurnID: turn, Attempt: 1})
	many := make([]string, MaxDeliveryParts+1)
	for i := range many {
		many[i] = "x"
	}
	base := func() PublishRequest { r := f.publishReq(res.ID, "x"); return r }
	cases := map[string]func(*PublishRequest){
		"legacy owner with result": func(r *PublishRequest) { r.Owner = OwnerLegacyAgentIPC },
		"unknown owner":            func(r *PublishRequest) { r.Owner = "x" },
		"no parts":                 func(r *PublishRequest) { r.Parts = nil },
		"too many parts":           func(r *PublishRequest) { r.Parts = textParts(many...) },
		"empty text":               func(r *PublishRequest) { r.Parts = []DeliveryPart{{Kind: PartText}} },
		"edit without target":      func(r *PublishRequest) { r.Parts = []DeliveryPart{{Kind: PartEdit, Payload: []byte("x")}} },
		"upload without artifact":  func(r *PublishRequest) { r.Parts = []DeliveryPart{{Kind: PartUpload}} },
		"upload with oversized caption": func(r *PublishRequest) {
			r.Parts = []DeliveryPart{{Kind: PartUpload, ArtifactRef: "a", Payload: make([]byte, MaxUploadCaptionBytes+1)}}
		},
		"unknown kind":     func(r *PublishRequest) { r.Parts = []DeliveryPart{{Kind: "x", Payload: []byte("x")}} },
		"expired":          func(r *PublishRequest) { r.ExpiresAt = time.Now().Add(-time.Second).UnixMilli() },
		"bad request hash": func(r *PublishRequest) { r.Request = &RequestIdentity{Key: "k", PayloadHash: "short"} },
		"bad visibility":   func(r *PublishRequest) { r.Conversation.Visibility = "" },
	}
	for name, mutate := range cases {
		r := base()
		mutate(&r)
		if _, err := f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: f.conv, ResultID: "missing", Owner: OwnerControllerResult, Parts: textParts("x")}); !errors.Is(err, ErrResultNotFound) {
		t.Errorf("missing result: %v", err)
	}
	max := make([]string, MaxDeliveryParts)
	for i := range max {
		max[i] = fmt.Sprint(i)
	}
	if d, err := f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, max...)); err != nil || len(d.Parts) != MaxDeliveryParts {
		t.Errorf("32 parts: %v", err)
	}
	upload := PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: []DeliveryPart{{Kind: PartUpload, ArtifactRef: "art-1"}, {Kind: PartEdit, Payload: []byte("e"), TargetMessageID: 5}}}
	if _, err := f.st.PublishDelivery(t.Context(), upload); !errors.Is(err, ErrInvalidDelivery) {
		t.Errorf("plain PublishDelivery must refuse uploads: %v", err)
	}
	edit := PublishRequest{Conversation: f.conv, Owner: OwnerLegacyAgentIPC, Parts: []DeliveryPart{{Kind: PartEdit, Payload: []byte("e"), TargetMessageID: 5}}}
	if d, err := f.st.PublishDelivery(t.Context(), edit); err != nil || d.Parts[0].Part.TargetMessageID != 5 {
		t.Errorf("edit: %+v %v", d, err)
	}
}

func TestResultScopeMustMatchDelivery(t *testing.T) {
	f := newDeliveryFixture(t)
	topic := f.turn("telegram", "1:1", 3, 1)
	topicConv, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 3, VisibilityConversation)
	res, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: topicConv, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: topic, Attempt: 1}}}, "t")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "x")); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("thread 3 result published to thread 0: %v", err)
	}
	private, _ := f.st.SnapshotConversation(t.Context(), "tg:1", 3, VisibilityPrivate)
	r := f.publishReq(res.ID, "x")
	r.Conversation = private
	if _, err = f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrScopeMismatch) {
		t.Fatalf("visibility widened or changed silently: %v", err)
	}
}

func TestIsolatedTaskResultPublishesWithoutForegroundEffects(t *testing.T) {
	f := newDeliveryFixture(t)
	task := f.turn("task", "t:1", 0, 1)
	res := f.freeze(ExecIsolatedTask, "report", ResultMember{TurnID: task, Attempt: 1})
	r := f.publishReq(res.ID, "report")
	r.TaskRef = "task-1"
	if _, err := f.st.PublishDelivery(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if historyCount(t, f.st) != 0 {
		t.Fatal("publishing an isolated task result touched foreground history")
	}
	if turn, _ := f.st.GetTurn(t.Context(), task); turn.ReplyMsgID != 0 {
		t.Fatal("publishing set reply_msg_id")
	}
}

func TestOutboxQuotas(t *testing.T) {
	f := newDeliveryFixture(t)
	if err := f.st.SetOutboxLimits(OutboxLimits{MaxUnresolvedGlobal: 3, MaxUnresolvedPerNamespace: 2}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 2}); err != nil {
		t.Fatal(err)
	}
	other, _ := f.st.SnapshotConversation(t.Context(), "tg:-2", 0, VisibilityConversation)
	pub := func(c ConversationScope, key string) (Delivery, error) {
		r := f.publishReq("", "x")
		r.Conversation, r.Owner = c, OwnerLegacyAgentIPC
		r.Request = &RequestIdentity{Key: key, PayloadHash: sha256Hex([]byte(key))}
		return f.st.PublishDelivery(t.Context(), r)
	}
	first, err := pub(f.conv, "1")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pub(f.conv, "2"); err != nil {
		t.Fatal(err)
	}
	if _, err = pub(f.conv, "3"); !errors.Is(err, ErrOutboxQuotaExceeded) {
		t.Fatalf("per-namespace quota: %v", err)
	}
	if _, err = pub(other, "4"); err != nil {
		t.Fatalf("other namespace blocked by a neighbour: %v", err)
	}
	if _, err = pub(other, "5"); !errors.Is(err, ErrOutboxQuotaExceeded) {
		t.Fatalf("global quota: %v", err)
	}
	if replay, err := pub(f.conv, "1"); err != nil || replay.ID != first.ID {
		t.Fatalf("replay blocked by quota pressure: %v", err)
	}
	if _, err = f.st.CancelDelivery(t.Context(), first.ID, "operator"); err != nil {
		t.Fatal(err)
	}
	if _, err = pub(f.conv, "3"); err != nil {
		t.Fatalf("terminal group still counted: %v", err)
	}
	if err = f.st.SetOutboxLimits(OutboxLimits{MaxUnresolvedGlobal: -1}); err == nil {
		t.Fatal("negative limit accepted")
	}
}

func TestDefaultOutboxLimitsFromSpec(t *testing.T) {
	st := newTestStore(t)
	if l := st.outboxLimits(); l.MaxUnresolvedGlobal != 10000 || l.MaxUnresolvedPerNamespace != 1000 {
		t.Fatalf("defaults %+v", l)
	}
}

func TestAggregateStateAndCancel(t *testing.T) {
	f := newDeliveryFixture(t)
	r := f.publishReq("", "a", "b", "c")
	r.Owner = OwnerLegacyAgentIPC
	d, err := f.st.PublishDelivery(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	set := func(ordinal int, state string) {
		t.Helper()
		if _, err := f.st.db.ExecContext(t.Context(), `UPDATE outbox_parts SET state=? WHERE delivery_id=? AND ordinal=?`, state, d.ID, ordinal); err != nil {
			t.Fatal(err)
		}
	}
	set(0, PartSent)
	cancelled, err := f.st.CancelDelivery(t.Context(), d.ID, "operator")
	if err != nil {
		t.Fatal(err)
	}
	if cancelled.State != DeliveryPartial || cancelled.Unresolved {
		t.Fatalf("partial acknowledgement lost: %s unresolved=%v", cancelled.State, cancelled.Unresolved)
	}
	if cancelled.Parts[1].State != PartCancelled || cancelled.Parts[1].ReasonCode != "operator" || cancelled.Parts[0].State != PartSent {
		t.Fatalf("parts %+v", cancelled.Parts)
	}
	cases := []struct {
		states []string
		state  string
		open   bool
	}{
		{[]string{PartSent, PartUncertain, PartPending}, DeliveryUncertain, true},
		{[]string{PartSent, PartSending, PartPending}, DeliverySending, true},
		{[]string{PartSent, PartPending, PartPending}, DeliveryPartial, true},
		{[]string{PartSent, PartSent, PartSent}, DeliverySent, false},
		{[]string{PartFailed, PartCancelled, PartExpired}, DeliveryFailed, false},
		{[]string{PartExpired, PartCancelled, PartCancelled}, DeliveryExpired, false},
		{[]string{PartCancelled, PartCancelled, PartCancelled}, DeliveryCancelled, false},
	}
	for _, c := range cases {
		counts := map[string]int{}
		for _, s := range c.states {
			counts[s]++
		}
		if got, open := aggregateState(counts, len(c.states)); got != c.state || open != c.open {
			t.Errorf("%v => %s/%v, want %s/%v", c.states, got, open, c.state, c.open)
		}
	}
	// Cancel never rewrites an uncertain part.
	r2 := f.publishReq("", "a", "b")
	r2.Owner, r2.RunRef = OwnerLegacyAgentIPC, "second"
	d2, _ := f.st.PublishDelivery(t.Context(), r2)
	if _, err = f.st.db.ExecContext(t.Context(), `UPDATE outbox_parts SET state='uncertain' WHERE delivery_id=? AND ordinal=0`, d2.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.CancelDelivery(t.Context(), d2.ID, "operator")
	if err != nil || got.State != DeliveryUncertain || got.Parts[0].State != PartUncertain {
		t.Fatalf("cancel touched uncertain part: %+v %v", got, err)
	}
	if _, err = f.st.CancelDelivery(t.Context(), "missing", "operator"); err == nil {
		t.Fatal("cancel of missing delivery succeeded")
	}
	if _, err = f.st.CancelDelivery(t.Context(), d.ID, ""); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("empty reason: %v", err)
	}
}

func TestDeliveryPlanIsImmutable(t *testing.T) {
	f := newDeliveryFixture(t)
	r := f.publishReq("", "secret")
	r.Owner = OwnerLegacyAgentIPC
	d, err := f.st.PublishDelivery(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`UPDATE delivery_groups SET plan_hash='x'`, `UPDATE delivery_groups SET thread_id=9`, `UPDATE delivery_groups SET expires_at=1`,
		`UPDATE delivery_groups SET delivery_owner='controller_result'`,
		`UPDATE outbox_parts SET payload=x'00'`, `UPDATE outbox_parts SET kind='edit'`, `UPDATE outbox_parts SET ordinal=5`, `UPDATE outbox_parts SET reply_markup='x'`,
	} {
		if _, err := f.st.db.ExecContext(t.Context(), stmt); err == nil {
			t.Errorf("%s succeeded", stmt)
		}
	}
	// Dispatch fields remain writable.
	if _, err = f.st.db.ExecContext(t.Context(), `UPDATE outbox_parts SET state='sending',attempts=1,remote_message_id=0`); err != nil {
		t.Fatalf("dispatch fields locked: %v", err)
	}
	got, _ := f.st.GetDelivery(t.Context(), d.ID)
	if string(got.Parts[0].Part.Payload) != "secret" {
		t.Fatal("payload changed")
	}
	if _, err = f.st.GetDelivery(t.Context(), "missing"); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("missing delivery: %v", err)
	}
}

func TestPlanHashCoversEveryField(t *testing.T) {
	f := newDeliveryFixture(t)
	base := f.publishReq("", "a")
	base.Owner = OwnerLegacyAgentIPC
	ref := planHash(base)
	mutations := map[string]func(*PublishRequest){
		"owner":    func(r *PublishRequest) { r.Owner = OwnerControllerResult },
		"actor":    func(r *PublishRequest) { r.ActorRef = "x" },
		"task":     func(r *PublishRequest) { r.TaskRef = "x" },
		"run":      func(r *PublishRequest) { r.RunRef = "x" },
		"payload":  func(r *PublishRequest) { r.Parts = textParts("b") },
		"parse":    func(r *PublishRequest) { r.Parts[0].ParseMode = "" },
		"markup":   func(r *PublishRequest) { r.Parts[0].ReplyMarkup = "{}" },
		"thread":   func(r *PublishRequest) { r.Conversation.ThreadID = 1 },
		"vis":      func(r *PublishRequest) { r.Conversation.Visibility = VisibilityPrivate },
		"split":    func(r *PublishRequest) { r.Parts = textParts("a", "") },
		"boundary": func(r *PublishRequest) { r.Parts = textParts("a"); r.Parts[0].Payload = []byte("a\x00") },
	}
	for name, mutate := range mutations {
		r := base
		r.Parts = append([]DeliveryPart(nil), base.Parts...)
		mutate(&r)
		if planHash(r) == ref {
			t.Errorf("%s does not change the plan hash", name)
		}
	}
	other := base
	other.Conversation.Scope.RegistrationRevision = 99
	other.ExpiresAt = 1
	if planHash(other) != ref {
		t.Error("revision/expiry must not change the plan hash")
	}
}

func TestCompactTerminalDeliveries(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	pub := func(key string, state string) Delivery {
		r := f.publishReq("", "payload-"+key)
		r.Owner = OwnerLegacyAgentIPC
		r.Request = &RequestIdentity{Key: key, PayloadHash: sha256Hex([]byte(key))}
		d, err := f.st.PublishDelivery(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET state=?,remote_message_id=11 WHERE delivery_id=?`, state, d.ID); err != nil {
			t.Fatal(err)
		}
		if _, err = f.st.db.ExecContext(ctx, `INSERT INTO outbox_attempts(part_id,fence,started_at) SELECT part_id,'f',1 FROM outbox_parts WHERE delivery_id=?`, d.ID); err != nil {
			t.Fatal(err)
		}
		if err = recomputeGroupStateForTest(f.st, d.ID); err != nil {
			t.Fatal(err)
		}
		return d
	}
	sent, uncertain, pending := pub("sent", PartSent), pub("unc", PartUncertain), pub("pend", PartPending)
	age := func(id string, by time.Duration) {
		if _, err := f.st.db.ExecContext(ctx, `UPDATE delivery_groups SET updated_at=? WHERE delivery_id=?`, time.Now().Add(-by).UnixMilli(), id); err != nil {
			t.Fatal(err)
		}
	}
	age(uncertain.ID, 90*24*time.Hour)
	age(pending.ID, 90*24*time.Hour)

	n, err := f.st.CompactTerminalDeliveries(ctx, time.Now(), 0, 10)
	if err != nil || n != 0 {
		t.Fatalf("fresh terminal group compacted: %d %v", n, err)
	}
	age(sent.ID, 31*24*time.Hour)
	n, err = f.st.CompactTerminalDeliveries(ctx, time.Now(), 0, 10)
	if err != nil || n != 1 {
		t.Fatalf("compacted %d %v", n, err)
	}
	got, _ := f.st.GetDelivery(ctx, sent.ID)
	if got.CompactedAt == 0 || got.State != DeliverySent || got.Parts[0].Part.Payload != nil || got.Parts[0].RemoteMessageID != 11 || got.PlanHash != sent.PlanHash {
		t.Fatalf("compaction lost truth or kept payload: %+v", got)
	}
	if countRows(t, f.st, `SELECT count(*) FROM outbox_attempts WHERE part_id=?`, sent.Parts[0].ID) != 0 {
		t.Fatal("attempt diagnostics kept")
	}
	for _, d := range []Delivery{uncertain, pending} {
		kept, _ := f.st.GetDelivery(ctx, d.ID)
		if kept.CompactedAt != 0 || kept.Parts[0].Part.Payload == nil {
			t.Fatalf("unresolved %s was compacted", d.ID)
		}
	}
	// The dedupe tombstone outlives the payload.
	r := f.publishReq("", "payload-sent")
	r.Owner = OwnerLegacyAgentIPC
	r.Request = &RequestIdentity{Key: "sent", PayloadHash: sha256Hex([]byte("sent"))}
	replay, err := f.st.PublishDelivery(ctx, r)
	if err != nil || replay.ID != sent.ID || replay.Created {
		t.Fatalf("replay after compaction: %+v %v", replay, err)
	}
	r.Request.PayloadHash = sha256Hex([]byte("changed"))
	if _, err = f.st.PublishDelivery(ctx, r); !errors.Is(err, ErrRequestConflict) {
		t.Fatalf("key reuse after compaction: %v", err)
	}
	if n, _ = f.st.CompactTerminalDeliveries(ctx, time.Now(), 0, 10); n != 0 {
		t.Fatalf("compacted twice: %d", n)
	}
}

func recomputeGroupStateForTest(st *Store, id string) error {
	_, err := st.withTx(context.Background(), func(q taskSQL) error {
		return recomputeGroupState(context.Background(), q, id, time.Now().UnixMilli())
	})
	return err
}

func TestPublishBoundsPayloadSizes(t *testing.T) {
	f := newDeliveryFixture(t)
	big := func(n int) []byte { return []byte(strings.Repeat("a", n)) }
	cases := map[string]DeliveryPart{
		"payload":      {Kind: PartText, Payload: big(MaxPartPayloadBytes + 1)},
		"edit payload": {Kind: PartEdit, Payload: big(MaxPartPayloadBytes + 1), TargetMessageID: 1},
		"markup":       {Kind: PartText, Payload: []byte("x"), ReplyMarkup: strings.Repeat("m", MaxReplyMarkupBytes+1)},
		"artifact":     {Kind: PartUpload, ArtifactRef: strings.Repeat("r", MaxArtifactRefBytes+1)},
		"parse mode":   {Kind: PartText, Payload: []byte("x"), ParseMode: strings.Repeat("p", MaxParseModeBytes+1)},
	}
	for name, part := range cases {
		r := f.publishReq("", "x")
		r.Owner, r.Parts = OwnerLegacyAgentIPC, []DeliveryPart{part}
		if _, err := f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("%s: %v", name, err)
		}
	}
	r := f.publishReq("", "x")
	r.Owner = OwnerLegacyAgentIPC
	r.Request = &RequestIdentity{Key: strings.Repeat("k", MaxRequestKeyBytes+1), PayloadHash: sha256Hex(nil)}
	if _, err := f.st.PublishDelivery(t.Context(), r); !errors.Is(err, ErrInvalidDelivery) {
		t.Errorf("long request key: %v", err)
	}
	r.Request = nil
	r.Parts = []DeliveryPart{{Kind: PartText, Payload: big(MaxPartPayloadBytes), ReplyMarkup: strings.Repeat("m", MaxReplyMarkupBytes), ParseMode: "HTML"}}
	if _, err := f.st.PublishDelivery(t.Context(), r); err != nil {
		t.Errorf("limits are inclusive: %v", err)
	}
}

func TestRejectionTombstoneCaps(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	if err := f.st.SetOutboxLimits(OutboxLimits{MaxRejectionsPerNamespace: 2, MaxRejectionsGlobal: 3}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RegisterChat(ctx, RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 2}); err != nil {
		t.Fatal(err)
	}
	other, _ := f.st.SnapshotConversation(ctx, "tg:-2", 0, VisibilityConversation)
	rec := func(sc ScopeRef, key string) bool {
		ok, err := f.st.RecordRequestRejection(ctx, sc, RequestIdentity{Key: key, PayloadHash: sha256Hex([]byte(key))}, "denied")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	if !rec(f.conv.Scope, "1") || !rec(f.conv.Scope, "2") {
		t.Fatal("rejections under the cap were not stored")
	}
	if rec(f.conv.Scope, "3") || rec(f.conv.Scope, "4") {
		t.Fatal("per-namespace cap exceeded")
	}
	if n, _ := f.st.DroppedRejections(ctx, f.conv.Scope.NamespaceID); n != 2 {
		t.Fatalf("dropped counter %d", n)
	}
	if !rec(other.Scope, "5") {
		t.Fatal("a flooding namespace blocked its neighbour (global cap not reached yet)")
	}
	if rec(other.Scope, "6") {
		t.Fatal("global cap exceeded")
	}
	if n, _ := f.st.DroppedRejections(ctx, other.Scope.NamespaceID); n != 1 {
		t.Fatalf("other dropped counter %d", n)
	}
	if !rec(f.conv.Scope, "1") {
		t.Fatal("replay of a stored rejection must keep succeeding at the cap")
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM ipc_message_requests WHERE outcome='rejected'`); n != 3 {
		t.Fatalf("stored tombstones %d", n)
	}
	if n, err := f.st.DroppedRejections(ctx, "none"); err != nil || n != 0 {
		t.Fatal(n, err)
	}
}

func TestPayloadClearedOnlyForResolvedDeliveries(t *testing.T) {
	f := newDeliveryFixture(t)
	ctx := t.Context()
	r := f.publishReq("", "keep")
	r.Owner = OwnerLegacyAgentIPC
	d, err := f.st.PublishDelivery(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET payload=NULL WHERE delivery_id=?`, d.ID); err == nil {
		t.Fatal("payload cleared on a pending part")
	}
	if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET state='uncertain' WHERE delivery_id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = recomputeGroupStateForTest(f.st, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET payload=NULL WHERE delivery_id=?`, d.ID); err == nil {
		t.Fatal("payload cleared on an uncertain part")
	}
	if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET state='sent' WHERE delivery_id=?`, d.ID); err != nil {
		t.Fatal(err)
	}
	if err = recomputeGroupStateForTest(f.st, d.ID); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.ExecContext(ctx, `UPDATE outbox_parts SET payload=NULL WHERE delivery_id=?`, d.ID); err != nil {
		t.Fatalf("payload not clearable once resolved: %v", err)
	}
	// Compaction itself is covered by TestCompactTerminalDeliveries.
}
