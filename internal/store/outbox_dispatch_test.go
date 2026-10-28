package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func (f *deliveryFixture) owner() *OutboxOwner {
	f.t.Helper()
	o, err := f.st.AcquireOutboxOwner(f.t.Context())
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(o.Release)
	return o
}

func (f *deliveryFixture) publish(texts ...string) Delivery {
	f.t.Helper()
	d, err := f.st.PublishDelivery(f.t.Context(), f.publishReq("", texts...))
	if err != nil {
		f.t.Fatal(err)
	}
	return d
}

func (f *deliveryFixture) otherConversation(jid, folder string) ConversationScope {
	f.t.Helper()
	if err := f.st.RegisterChat(f.t.Context(), RegisteredChat{JID: jid, Folder: folder, AddedAt: 1}); err != nil {
		f.t.Fatal(err)
	}
	c, err := f.st.SnapshotConversation(f.t.Context(), jid, 0, VisibilityConversation)
	if err != nil {
		f.t.Fatal(err)
	}
	return c
}

func mustClaim(t *testing.T, o *OutboxOwner, at time.Time) OutboxClaim {
	t.Helper()
	c, ok, err := o.ClaimNext(t.Context(), at)
	if err != nil || !ok {
		t.Fatalf("ClaimNext ok=%v err=%v", ok, err)
	}
	return c
}

func noClaim(t *testing.T, o *OutboxOwner, at time.Time) {
	t.Helper()
	if c, ok, err := o.ClaimNext(t.Context(), at); err != nil || ok {
		t.Fatalf("unexpected claim %+v ok=%v err=%v", c, ok, err)
	}
}

func TestOwnerLockIsExclusiveAndFencesOlderOwner(t *testing.T) {
	f := newDeliveryFixture(t)
	first := f.owner()
	if _, err := f.st.AcquireOutboxOwner(t.Context()); !errors.Is(err, ErrOutboxLocked) {
		t.Fatalf("second owner: %v", err)
	}
	f.publish("a")
	claim := mustClaim(t, first, time.Now())
	first.Release()
	second := f.owner()
	if second.Fence() == first.Fence() {
		t.Fatal("fence not replaced")
	}
	if err := first.AckPart(t.Context(), claim, 10); !errors.Is(err, ErrOutboxFenced) {
		t.Fatalf("superseded owner ack: %v", err)
	}
	if _, _, err := first.ClaimNext(t.Context(), time.Now()); !errors.Is(err, ErrOutboxFenced) {
		t.Fatalf("superseded owner claim: %v", err)
	}
}

func TestClaimCommitsSendingAttemptAndFenceBeforeCall(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a", "b")
	c := mustClaim(t, o, time.Now())
	if c.Ordinal != 0 || c.Attempt != 1 || string(c.Part.Payload) != "a" || c.Fence != o.Fence() {
		t.Fatalf("claim %+v", c)
	}
	diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if err != nil || diag.State != DeliverySending || diag.Parts[0].State != PartSending {
		t.Fatalf("not committed as sending: %+v %v", diag, err)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM outbox_attempts WHERE part_id=? AND fence=? AND ended_at IS NULL`, c.PartID, o.Fence()); n != 1 {
		t.Fatalf("open attempts = %d", n)
	}
	noClaim(t, o, time.Now()) // one in-flight part per conversation
}

func TestOrderingAcrossDeliveriesInOneConversation(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	f.publish("first-1", "first-2")
	f.publish("second")
	var got []string
	for i := 0; i < 3; i++ {
		c := mustClaim(t, o, time.Now())
		got = append(got, string(c.Part.Payload))
		if err := o.AckPart(t.Context(), c, int64(100+i)); err != nil {
			t.Fatal(err)
		}
	}
	if strings.Join(got, ",") != "first-1,first-2,second" {
		t.Fatalf("order %v", got)
	}
}

func TestConversationsProgressIndependently(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	other := f.otherConversation("tg:2", "other")
	f.publish("a")
	if _, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: other, Owner: OwnerControllerResult, Parts: textParts("b")}); err != nil {
		t.Fatal(err)
	}
	c1 := mustClaim(t, o, time.Now())
	c2 := mustClaim(t, o, time.Now()) // second conversation is not blocked by the first one's in-flight part
	if c1.Conversation.Scope.JID == c2.Conversation.Scope.JID {
		t.Fatal("two parts of one conversation in flight")
	}
}

func TestUncertainHeadBlocksOnlyItsConversation(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	other := f.otherConversation("tg:2", "other")
	a := f.publish("a1", "a2")
	f.publish("a-next")
	if _, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: other, Owner: OwnerControllerResult, Parts: textParts("b1", "b2")}); err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if c.Conversation.Scope.JID != "tg:1" {
		c = mustClaim(t, o, time.Now())
	}
	if err := o.MarkUncertain(t.Context(), c, "timeout", "class=timeout"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		b := mustClaim(t, o, time.Now())
		if b.Conversation.Scope.JID != "tg:2" {
			t.Fatalf("claimed %s behind an uncertain head", b.Conversation.Scope.JID)
		}
		if err := o.AckPart(t.Context(), b, int64(50+i)); err != nil {
			t.Fatal(err)
		}
	}
	noClaim(t, o, time.Now())
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), a.ID)
	if diag.State != DeliveryUncertain || !diag.Unresolved || diag.Parts[1].State != PartPending {
		t.Fatalf("diag %+v", diag)
	}
}

func TestAckIsAtomicAndReportsPartialAcknowledgement(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a", "b", "c")
	c := mustClaim(t, o, time.Now())
	if err := o.AckPart(t.Context(), c, 77); err != nil {
		t.Fatal(err)
	}
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryPartial || diag.Acknowledged != 1 || diag.AckedPrefix != 1 || diag.Parts[0].RemoteMessageID != 77 {
		t.Fatalf("diag %+v", diag)
	}
	if !strings.Contains(diag.Summary(), "1 of 3 parts acknowledged") {
		t.Fatal(diag.Summary())
	}
	if err := o.AckPart(t.Context(), c, 78); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("double ack: %v", err)
	}
	if err := o.AckPart(t.Context(), mustClaim(t, o, time.Now()), 0); err == nil {
		t.Fatal("ack without remote id accepted")
	}
}

func TestReplyMemoSetOnlyWhenWholePlanAcknowledged(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	turn := f.turn("telegram", "m:1", 0, 1)
	res := f.freeze(ExecInteractive, "answer", ResultMember{TurnID: turn, Attempt: 1})
	if _, err := f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "p1", "p2")); err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if err := o.AckPart(t.Context(), c, 201); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetTurn(t.Context(), turn); got.ReplyMsgID != 0 {
		t.Fatal("memo set after a partial acknowledgement")
	}
	if err := o.AckPart(t.Context(), mustClaim(t, o, time.Now()), 202); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetTurn(t.Context(), turn); got.ReplyMsgID != 202 {
		t.Fatalf("memo = %d", got.ReplyMsgID)
	}
}

func TestIsolatedTaskAckNeverSetsMemo(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	task := f.turn("task", "t:1", 0, 1)
	res := f.freeze(ExecIsolatedTask, "report", ResultMember{TurnID: task, Attempt: 1})
	if _, err := f.st.PublishDelivery(t.Context(), f.publishReq(res.ID, "report")); err != nil {
		t.Fatal(err)
	}
	if err := o.AckPart(t.Context(), mustClaim(t, o, time.Now()), 5); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.st.GetTurn(t.Context(), task); got.ReplyMsgID != 0 {
		t.Fatal("task turn got a foreground memo")
	}
}

func TestReleaseUnsentSchedulesRetryWithinBudgetAndExpiry(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a")
	now := time.Now()
	c := mustClaim(t, o, now)
	st, err := o.ReleaseUnsent(t.Context(), c, now.Add(time.Minute), "rate_limited", "http=429 retry_after=60", false)
	if err != nil || st != PartPending {
		t.Fatalf("state %s err %v", st, err)
	}
	noClaim(t, o, now.Add(30*time.Second))
	if due, ok, _ := o.NextDue(t.Context(), now); !ok || due.Before(now.Add(59*time.Second)) {
		t.Fatalf("next due %v %v", due, ok)
	}
	c = mustClaim(t, o, now.Add(time.Minute+time.Second))
	if c.Attempt != 2 {
		t.Fatalf("attempt %d", c.Attempt)
	}
	// Retry-after past the delivery expiry expires the part instead of waiting.
	st, err = o.ReleaseUnsent(t.Context(), c, time.UnixMilli(d.ExpiresAt).Add(time.Second), "rate_limited", "http=429", false)
	if err != nil || st != PartExpired {
		t.Fatalf("state %s err %v", st, err)
	}
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryExpired || diag.Unresolved {
		t.Fatalf("diag %+v", diag)
	}
}

func TestReleaseUnsentStopsAtFiveAttempts(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a", "b")
	now := time.Now()
	var st string
	for i := 1; i <= MaxPartAttempts; i++ {
		c := mustClaim(t, o, now)
		if c.Attempt != i {
			t.Fatalf("attempt %d want %d", c.Attempt, i)
		}
		var err error
		if st, err = o.ReleaseUnsent(t.Context(), c, now, "rate_limited", "http=429", false); err != nil {
			t.Fatal(err)
		}
	}
	if st != PartFailed {
		t.Fatalf("fifth release state %s", st)
	}
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.Parts[0].ReasonCode != "retry_budget_exhausted" || diag.Parts[1].State != PartCancelled || diag.Unresolved {
		t.Fatalf("diag %+v", diag)
	}
	noClaim(t, o, now)
}

func TestRefundedReleaseDoesNotSpendBudget(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	f.publish("a")
	now := time.Now()
	for i := 0; i < MaxPartAttempts+2; i++ {
		c := mustClaim(t, o, now)
		if c.Attempt != 1 {
			t.Fatalf("attempt %d", c.Attempt)
		}
		if _, err := o.ReleaseUnsent(t.Context(), c, now, "shutdown_before_dispatch", "", true); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFailPartCancelsLaterPartsButKeepsAckedPrefix(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a", "b", "c")
	if err := o.AckPart(t.Context(), mustClaim(t, o, time.Now()), 1); err != nil {
		t.Fatal(err)
	}
	if err := o.FailPart(t.Context(), mustClaim(t, o, time.Now()), "forbidden", "http=403"); err != nil {
		t.Fatal(err)
	}
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryPartial || diag.Unresolved || diag.Parts[0].State != PartSent || diag.Parts[1].State != PartFailed || diag.Parts[2].State != PartCancelled {
		t.Fatalf("diag %+v", diag)
	}
	noClaim(t, o, time.Now())
}

func TestStaleScopeCancelsInsteadOfSending(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a", "b")
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	noClaim(t, o, time.Now())
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryCancelled || diag.Parts[0].ReasonCode != "scope_stale" || diag.Unresolved {
		t.Fatalf("diag %+v", diag)
	}
}

func TestExpiredPendingPartsAreNotSent(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	d := f.publish("a")
	noClaim(t, o, time.UnixMilli(d.ExpiresAt).Add(time.Second))
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryExpired {
		t.Fatalf("diag %+v", diag)
	}
}

func TestSweepMakesSendingUncertainAndNeverReclaims(t *testing.T) {
	f := newDeliveryFixture(t)
	first := f.owner()
	d := f.publish("a", "b")
	if err := first.AckPart(t.Context(), mustClaim(t, first, time.Now()), 9); err != nil {
		t.Fatal(err)
	}
	mustClaim(t, first, time.Now()) // crash: no ack, no uncertain mark
	first.Release()
	second := f.owner()
	n, err := second.SweepSending(t.Context())
	if err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	diag, _ := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if diag.State != DeliveryUncertain || diag.Parts[0].State != PartSent || diag.Parts[1].State != PartUncertain || diag.Parts[1].ReasonCode != "restart_while_sending" {
		t.Fatalf("diag %+v", diag)
	}
	if countRows(t, f.st, `SELECT count(*) FROM outbox_attempts WHERE ended_at IS NULL`) != 0 {
		t.Fatal("attempt left open")
	}
	noClaim(t, second, time.Now().Add(time.Hour))
}

func TestStoredReasonsAndDiagnosticsAreSanitized(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	f.publish("a")
	c := mustClaim(t, o, time.Now())
	raw := "Post https://api.telegram.org/bot123456:SECRETVALUE/sendMessage: dial tcp: i/o timeout"
	if err := o.MarkUncertain(t.Context(), c, raw, raw); err != nil {
		t.Fatal(err)
	}
	var reason, diag string
	if err := f.st.db.QueryRowContext(t.Context(), `SELECT reason_code FROM outbox_parts`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if err := f.st.db.QueryRowContext(t.Context(), `SELECT diagnostic FROM outbox_attempts`).Scan(&diag); err != nil {
		t.Fatal(err)
	}
	if reason != "invalid_reason" || diag != "<redacted>" {
		t.Fatalf("reason %q diag %q", reason, diag)
	}
	for _, ok := range []string{"http=429 retry_after=3", "class=timeout"} {
		if sanitizeDiagnostic(ok) != ok {
			t.Fatalf("%q was altered", ok)
		}
	}
	if sanitizeDiagnostic("token=abc") != "<redacted>" {
		t.Fatal("secret word kept")
	}
}

func TestSweepOrphanedSendingSparesLiveAndRecentParts(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	other := f.otherConversation("tg:2", "other")
	live := f.publish("live")
	if _, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: other, Owner: OwnerControllerResult, Parts: textParts("orphan")}); err != nil {
		t.Fatal(err)
	}
	a := mustClaim(t, o, time.Now())
	b := mustClaim(t, o, time.Now())
	liveClaim, orphan := a, b
	if string(a.Part.Payload) != "live" {
		liveClaim, orphan = b, a
	}
	time.Sleep(20 * time.Millisecond)
	if n, err := o.SweepOrphanedSending(t.Context(), []string{liveClaim.PartID}, time.Hour); err != nil || n != 0 {
		t.Fatalf("recent parts must be spared: %d %v", n, err)
	}
	n, err := o.SweepOrphanedSending(t.Context(), []string{liveClaim.PartID}, 0)
	if err != nil || n != 1 {
		t.Fatalf("swept %d %v", n, err)
	}
	if d := mustDiag(t, f, live.ID); d.Parts[0].State != PartSending {
		t.Fatalf("live part touched: %+v", d)
	}
	var state, reason string
	if err := f.st.db.QueryRowContext(t.Context(), `SELECT state,reason_code FROM outbox_parts WHERE part_id=?`, orphan.PartID).Scan(&state, &reason); err != nil {
		t.Fatal(err)
	}
	if state != PartUncertain || reason != "orphaned_while_sending" {
		t.Fatalf("orphan %s %s", state, reason)
	}
	if err := o.AckPart(t.Context(), liveClaim, 3); err != nil {
		t.Fatalf("live part could not be acknowledged: %v", err)
	}
}

func mustDiag(t *testing.T, f *deliveryFixture, id string) DeliveryDiagnostic {
	t.Helper()
	d, err := f.st.DeliveryDiagnostic(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestClaimKeepsGoingWhenStaleCancelCapIsHit(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	for i := 0; i < maxStaleSkips+6; i++ {
		f.publish("stale")
	}
	other := f.otherConversation("tg:2", "other")
	if _, err := f.st.PublishDelivery(t.Context(), PublishRequest{Conversation: other, Owner: OwnerControllerResult, Parts: textParts("fresh")}); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1, Name: "renamed"}); err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if string(c.Part.Payload) != "fresh" {
		t.Fatalf("claimed %q", c.Part.Payload)
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM outbox_parts WHERE reason_code='scope_stale'`); n != maxStaleSkips+6 {
		t.Fatalf("cancelled %d", n)
	}
}
