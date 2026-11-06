package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func repairAttemptFixture(t *testing.T) (*deliveryFixture, ControlMessage) {
	t.Helper()
	f := topicDeliveryFixture(t)
	id := f.turn("telegram", "repair", 7, 1)
	c := ControlMessage{ChatID: 1, MessageID: 77, TurnID: id, Token: "control", Scope: f.conv.Scope, ThreadID: 7}
	if err := f.st.BindControlMessage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	c, err := f.st.GetControlMessage(t.Context(), 1, 77)
	if err != nil {
		t.Fatal(err)
	}
	return f, c
}
func TestRepairAttemptExactSettlementAndRevision(t *testing.T) {
	f, c := repairAttemptFixture(t)
	a, err := f.st.BeginControlRepair(t.Context(), c, strings.Repeat("a", 64), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.BeginControlRepair(t.Context(), c, strings.Repeat("b", 64), true); !errors.Is(err, ErrControlRepairPending) {
		t.Fatal(err)
	}
	wrong := a
	wrong.RequestHash = strings.Repeat("b", 64)
	if err = f.st.SettleControlRepair(t.Context(), wrong, true); !errors.Is(err, ErrControlRepairPending) {
		t.Fatal(err)
	}
	if err = f.st.CheckpointControlMessage(t.Context(), 1, 77, "new", false, false); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SettleControlRepair(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	got, _ := f.st.GetControlMessage(t.Context(), 1, 77)
	if got.Terminal || got.Acknowledged != 0 {
		t.Fatal("late receipt retired successor", got)
	}
	if err = f.st.SettleControlRepair(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SettleControlRepair(t.Context(), a, false); !errors.Is(err, ErrControlRepairPending) {
		t.Fatal(err)
	}
	if err = f.st.ControlRepairBarrier(t.Context(), 1, 77); err != nil {
		t.Fatal(err)
	}
}
func TestRepairPendingSurvivesDeletionRotationAndReopen(t *testing.T) {
	f, c := repairAttemptFixture(t)
	a, err := f.st.BeginControlRepair(t.Context(), c, strings.Repeat("a", 64), true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.Exec(`DELETE FROM telegram_control_repair_attempts`); err == nil {
		t.Fatal("deleted pending")
	}
	if _, err = f.st.db.Exec(`UPDATE telegram_control_repair_attempts SET request_hash=?`, strings.Repeat("b", 64)); err == nil {
		t.Fatal("mutated evidence")
	}
	if _, err = f.st.db.Exec(`DELETE FROM telegram_control_messages`); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, f.st, c.Scope, 7)
	if err = f.st.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
	ev, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || ev.ControlRepairsPending != 1 || !ev.Unsettled() {
		t.Fatal(ev, err)
	}
	if err = f.st.RegisterChat(t.Context(), RegisteredChat{JID: "tg:1", Folder: "owner", Name: "new", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	path := f.st.path
	f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.ControlRepairBarrier(t.Context(), 1, 77); !errors.Is(err, ErrControlRepairPending) {
		t.Fatal(err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 78); err != nil {
		t.Fatal("sibling blocked", err)
	}
	scope, err := st.SnapshotScope(t.Context(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	rebound := c
	rebound.Scope = scope
	rebound.Token = "successor"
	if err = st.BindControlMessage(t.Context(), rebound); err != nil {
		t.Fatal(err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 77); !errors.Is(err, ErrControlRepairPending) {
		t.Fatal(err)
	}
	// Original exact receipt remains evidence after rotation, without relabeling it.
	if err = st.SettleControlRepair(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	next, _ := st.GetControlMessage(t.Context(), 1, 77)
	if next.Terminal || next.Acknowledged != 0 {
		t.Fatal("retired rebound control", next)
	}
}
func TestRepairBarrierSkipsOutboxWithoutSpendingBudget(t *testing.T) {
	f, c := repairAttemptFixture(t)
	a, err := f.st.BeginControlRepair(t.Context(), c, strings.Repeat("a", 64), false)
	if err != nil {
		t.Fatal(err)
	}
	r := f.freeze(ExecInteractive, "final", ResultMember{TurnID: c.TurnID, Attempt: 1})
	req := f.publishReq(r.ID, "final")
	req.Parts[0].Kind = PartEdit
	req.Parts[0].TargetMessageID = 77
	d, err := f.st.PublishDelivery(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	o := f.owner()
	for i := 0; i < MaxPartAttempts+2; i++ {
		if _, ok, err := o.ClaimNext(t.Context(), time.Now()); err != nil || ok {
			t.Fatal(ok, err)
		}
	}
	got, err := f.st.GetDelivery(t.Context(), d.ID)
	if err != nil || got.Parts[0].Attempts != 0 || got.Parts[0].State != PartPending {
		t.Fatal(got, err)
	}
	sibling := f.publishReq("", "sibling")
	sibling.Conversation.ThreadID = 8
	if _, err = f.st.PublishDelivery(t.Context(), sibling); err != nil {
		t.Fatal(err)
	}
	other, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok || other.Conversation.ThreadID != 8 {
		t.Fatal(other, ok, err)
	}
	if err = o.AckPart(t.Context(), other, 88); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SettleControlRepair(t.Context(), a, false); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := o.ClaimNext(t.Context(), time.Now()); err != nil || !ok {
		t.Fatal(ok, err)
	}
}

func TestRepairSchemaMigrationPreservesPopulatedDatabase(t *testing.T) {
	f, id, _, d, o := actionFixture(t)
	o.Release()
	c := ControlMessage{ChatID: 1, MessageID: 55, TurnID: id, Token: "control", Scope: f.conv.Scope, ThreadID: 7}
	if err := f.st.BindControlMessage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	old, _ := f.st.GetControlMessage(t.Context(), 1, 55)
	if _, err := f.st.db.Exec(`DROP TABLE telegram_control_repair_attempts`); err != nil {
		t.Fatal(err)
	}
	ev, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || ev.ControlRepairsPending != 0 {
		t.Fatal(ev, err)
	}
	path := f.st.path
	f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	got, err := st.GetDelivery(t.Context(), d.ID)
	if err != nil || got.PlanHash != d.PlanHash {
		t.Fatal(got, err)
	}
	control, err := st.GetControlMessage(t.Context(), 1, 55)
	if err != nil || control != old {
		t.Fatal(control, err)
	}
	info, err := InspectDB(t.Context(), path)
	if err != nil || len(info.UnknownTables) != 0 {
		t.Fatal(info, err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 55); err != nil {
		t.Fatal(err)
	}
}
