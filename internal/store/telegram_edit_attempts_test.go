package store

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func directRequest(chat int64, msg, thread int) TelegramEditRequest {
	return TelegramEditRequest{ChatID: chat, MessageID: msg, SourceThread: thread, Method: "text", RequestHash: strings.Repeat("a", 64)}
}
func beginDirect(t *testing.T, f *deliveryFixture, c ControlMessage) TelegramEditAttempt {
	t.Helper()
	o, covered, err := f.st.ResolveDirectEditOwner(t.Context(), c.ChatID, c.MessageID)
	if err != nil || !covered {
		t.Fatal(o, covered, err)
	}
	a, err := f.st.BeginTelegramEdit(t.Context(), directRequest(c.ChatID, c.MessageID, c.ThreadID), o, false)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestDirectEditPendingRestartRawTargetAndExactSettlement(t *testing.T) {
	f, c := repairAttemptFixture(t)
	a := beginDirect(t, f, c)
	if _, err := f.st.BeginControlRepair(t.Context(), c, strings.Repeat("b", 64), false); err == nil {
		t.Fatal("repair crossed direct pending")
	}
	if _, err := f.st.db.Exec(`DELETE FROM telegram_edit_attempts`); err == nil {
		t.Fatal("pending erased")
	}
	wrong := a
	wrong.Method = "markup"
	if err := f.st.SettleTelegramEdit(t.Context(), wrong, true); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	d := beginTopic(t, f.st, c.Scope, 7)
	if err := f.st.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
	ev, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || ev.DirectEditsPending != 1 {
		t.Fatal(ev, err)
	}
	if _, err = f.st.db.Exec(`DELETE FROM telegram_control_messages`); err != nil {
		t.Fatal(err)
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
	if err = st.ControlRepairBarrier(t.Context(), 1, 77); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 78); err != nil {
		t.Fatal(err)
	}
	if err = st.SettleTelegramEdit(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if err = st.SettleTelegramEdit(t.Context(), a, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
}
func TestDirectAdmissionCannotDowngradeAfterRegistrationRemoval(t *testing.T) {
	f, c := repairAttemptFixture(t)
	o, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 77)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.db.Exec(`DELETE FROM registered_chats WHERE jid='tg:1'`); err != nil {
		t.Fatal(err)
	}
	a, err := f.st.BeginTelegramEdit(t.Context(), directRequest(1, 77, c.ThreadID), o, false)
	if err == nil || a.ID != "" {
		t.Fatal("downgraded to unjournaled HTTP", a, err)
	}
}
func TestDirectProvenanceAndFinalCallbackAuthority(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	if _, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 999); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("historical cache-free target", err)
	}
	ackAction(t, o, 55)
	owner, covered, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 55)
	if err != nil || !covered {
		t.Fatal(owner, covered, err)
	}
	req := directRequest(1, 55, 7)
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("overwrote final", err)
	}
	req.Method = "markup"
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("unqualified markup", err)
	}
	req.FinalTurn = id
	a, err := f.st.BeginTelegramEdit(t.Context(), req, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.SettleTelegramEdit(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	req.SourceThread = 8
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrScopeMismatch) {
		t.Fatal(err)
	}
}
func TestDirectOutboxMutualExclusionAndNoAttemptBurn(t *testing.T) {
	for _, first := range []string{"direct", "outbox"} {
		t.Run(first, func(t *testing.T) {
			f, c := repairAttemptFixture(t)
			owner, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 77)
			if err != nil {
				t.Fatal(err)
			}
			var a TelegramEditAttempt
			if first == "direct" {
				a = beginDirect(t, f, c)
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
			if first == "outbox" {
				claim, ok, e := o.ClaimNext(t.Context(), time.Now())
				if e != nil || !ok {
					t.Fatal(ok, e)
				}
				for _, uncertain := range []bool{false, true} {
					if uncertain {
						if e = o.MarkUncertain(t.Context(), claim, "unknown", ""); e != nil {
							t.Fatal(e)
						}
					}
					if _, e = f.st.BeginTelegramEdit(t.Context(), directRequest(1, 77, 7), owner, false); !errors.Is(e, ErrTelegramEditHeld) {
						t.Fatal("direct crossed outbox", e)
					}
				}
				return
			}
			for i := 0; i < MaxPartAttempts+1; i++ {
				if _, ok, e := o.ClaimNext(t.Context(), time.Now()); e != nil || ok {
					t.Fatal(ok, e)
				}
			}
			got, e := f.st.GetDelivery(t.Context(), d.ID)
			if e != nil || got.Parts[0].Attempts != 0 {
				t.Fatal(got, e)
			}
			sibling := f.publishReq("", "sibling")
			sibling.Conversation.ThreadID = 8
			if _, e = f.st.PublishDelivery(t.Context(), sibling); e != nil {
				t.Fatal(e)
			}
			other, ok, e := o.ClaimNext(t.Context(), time.Now())
			if e != nil || !ok || other.Conversation.ThreadID != 8 {
				t.Fatal(other, ok, e)
			}
			if e = o.AckPart(t.Context(), other, 88); e != nil {
				t.Fatal(e)
			}
			if e = f.st.SettleTelegramEdit(t.Context(), a, true); e != nil {
				t.Fatal(e)
			}
			if _, ok, e = o.ClaimNext(t.Context(), time.Now()); e != nil || !ok {
				t.Fatal(ok, e)
			}
		})
	}
}
func TestDirectSchemaMigrationKeepsPriorLedgersAndHashes(t *testing.T) {
	f, _, _, d, o := actionFixture(t)
	o.Release()
	if _, err := f.st.db.Exec(`DROP TABLE telegram_edit_attempts`); err != nil {
		t.Fatal(err)
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
	info, err := InspectDB(t.Context(), path)
	if err != nil || len(info.UnknownTables) != 0 {
		t.Fatal(info, err)
	}
}
