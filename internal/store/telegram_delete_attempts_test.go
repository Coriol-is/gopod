package store

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func deleteRequest(chat int64, msg, thread int) TelegramEditRequest {
	r := directRequest(chat, msg, thread)
	r.Method = "delete"
	return r
}
func beginDelete(t *testing.T, f *deliveryFixture, c ControlMessage) TelegramDeleteAttempt {
	t.Helper()
	o, covered, err := f.st.ResolveDirectEditOwner(t.Context(), c.ChatID, c.MessageID)
	if err != nil || !covered {
		t.Fatal(o, covered, err)
	}
	a, err := f.st.BeginTelegramDelete(t.Context(), deleteRequest(c.ChatID, c.MessageID, c.ThreadID), o)
	if err != nil {
		t.Fatal(err)
	}
	return a
}
func TestDeletePendingRestartRetentionAndTombstone(t *testing.T) {
	f, c := repairAttemptFixture(t)
	a := beginDelete(t, f, c)
	if _, err := f.st.BeginControlRepair(t.Context(), c, strings.Repeat("b", 64), false); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal(err)
	}
	if _, err := f.st.db.Exec(`DELETE FROM telegram_delete_attempts`); err == nil {
		t.Fatal("pending deleted")
	}
	d := beginTopic(t, f.st, c.Scope, 7)
	if err := f.st.TopicMessageOwnershipIdle(t.Context(), d); !errors.Is(err, ErrMessageOwnership) {
		t.Fatal(err)
	}
	ev, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || ev.DeletesPending != 1 {
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
	if err = st.ControlRepairBarrier(t.Context(), 1, 77); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal(err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 78); err != nil {
		t.Fatal(err)
	}
	wrong := a
	wrong.RequestHash = strings.Repeat("b", 64)
	if err = st.SettleTelegramDelete(t.Context(), wrong, true); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal(err)
	}
	if err = st.SettleTelegramDelete(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if err = st.SettleTelegramDelete(t.Context(), a, true); err != nil {
		t.Fatal(err)
	}
	if err = st.SettleTelegramDelete(t.Context(), a, false); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal(err)
	}
	if err = st.ControlRepairBarrier(t.Context(), 1, 77); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal("ACK lost tombstone", err)
	}
	if _, err = st.db.Exec(`DELETE FROM telegram_delete_attempts`); err == nil {
		t.Fatal("ACK erased")
	}
	ev, err = ReadWorkEvidence(t.Context(), path)
	if err != nil || ev.DeletesPending != 0 {
		t.Fatal(ev, err)
	}
}
func TestDeleteAckRetiresOnlyExactControl(t *testing.T) {
	for _, successor := range []bool{false, true} {
		t.Run(map[bool]string{false: "exact", true: "successor"}[successor], func(t *testing.T) {
			f, c := repairAttemptFixture(t)
			a := beginDelete(t, f, c)
			if successor {
				if err := f.st.CheckpointControlMessage(t.Context(), 1, 77, "new", false, false); err != nil {
					t.Fatal(err)
				}
			}
			if err := f.st.SettleTelegramDelete(t.Context(), a, true); err != nil {
				t.Fatal(err)
			}
			got, err := f.st.GetControlMessage(t.Context(), 1, 77)
			if successor {
				if err != nil || got.Text != "new" || got.Acknowledged != 0 {
					t.Fatal(got, err)
				}
			} else if err == nil {
				t.Fatal("deleted exact control survived", got)
			}
			rows, err := f.st.ControlMessages(t.Context())
			if err != nil || len(rows) != 0 {
				t.Fatal("tombstone repair candidate", rows, err)
			}
		})
	}
}
func TestDeleteAdmissionAndPublishedGuard(t *testing.T) {
	f, c := repairAttemptFixture(t)
	o, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 77)
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"hash", "source", "owner", "method"} {
		r := deleteRequest(1, 77, 7)
		expected := o
		switch kind {
		case "hash":
			r.RequestHash = strings.Repeat("z", 64)
		case "source":
			r.SourceThread = 8
		case "owner":
			expected.Evidence = "fake"
		case "method":
			r.Method = "text"
		}
		if a, err := f.st.BeginTelegramDelete(t.Context(), r, expected); err == nil || a.ID != "" {
			t.Fatal(kind, a, err)
		}
	}
	if _, err = f.st.db.Exec(`DELETE FROM registered_chats WHERE jid='tg:1'`); err != nil {
		t.Fatal(err)
	}
	if a, err := f.st.BeginTelegramDelete(t.Context(), deleteRequest(1, 77, c.ThreadID), o); err == nil || a.ID != "" {
		t.Fatal(a, err)
	}
	f2, _, _, _, out := actionFixture(t)
	ackAction(t, out, 55)
	creation, err := f2.st.BeginTelegramMessageOwner(t.Context(), 1, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = f2.st.AcknowledgeTelegramMessageOwner(t.Context(), creation, 55); err != nil {
		t.Fatal(err)
	}
	owner, _, err := f2.st.ResolveDirectEditOwner(t.Context(), 1, 55)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(owner.Evidence, "creation:") {
		t.Fatal("did not test creation provenance", owner)
	}
	if _, err = f2.st.BeginTelegramDelete(t.Context(), deleteRequest(1, 55, 7), owner); !errors.Is(err, ErrTelegramDeleteHeld) {
		t.Fatal("published deletion", err)
	}
}
func TestDeleteOutboxMutualExclusionAndNoAttemptBurn(t *testing.T) {
	for _, first := range []string{"direct", "outbox"} {
		t.Run(first, func(t *testing.T) {
			f, c := repairAttemptFixture(t)
			owner, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 77)
			if err != nil {
				t.Fatal(err)
			}
			var a TelegramDeleteAttempt
			if first == "direct" {
				a = beginDelete(t, f, c)
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
					if _, e = f.st.BeginTelegramDelete(t.Context(), deleteRequest(1, 77, 7), owner); !errors.Is(e, ErrTelegramEditHeld) {
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
			if e = f.st.SettleTelegramDelete(t.Context(), a, true); e != nil {
				t.Fatal(e)
			}
			if _, ok, e = o.ClaimNext(t.Context(), time.Now()); e != nil || ok {
				t.Fatal(ok, e)
			}
		})
	}
}
func TestDeleteSchemaMigrationKeepsPriorLedgersAndHashes(t *testing.T) {
	f, _, _, d, o := actionFixture(t)
	o.Release()
	if _, err := f.st.db.Exec(`DROP TABLE telegram_delete_attempts`); err != nil {
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

func TestDeleteTombstonesCannotStarveControlScan(t *testing.T) {
	f, c := repairAttemptFixture(t)
	if _, err := f.st.db.Exec(`DELETE FROM telegram_control_messages WHERE message_id=77`); err != nil {
		t.Fatal(err)
	}
	c.MessageID = 2000
	if err := f.st.BindControlMessage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 101; i++ {
		row := c
		row.MessageID = 1000 + i
		row.Token = fmt.Sprintf("control-%d", i)
		if err := f.st.BindControlMessage(t.Context(), row); err != nil {
			t.Fatal(err)
		}
		row, err := f.st.GetControlMessage(t.Context(), 1, row.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		a := beginDelete(t, f, row)
		// Advance the control so ACK cannot retire its successor; the tombstone must
		// exclude that successor before LIMIT rather than starving unrelated repairs.
		if err = f.st.CheckpointControlMessage(t.Context(), 1, row.MessageID, "later", false, false); err != nil {
			t.Fatal(err)
		}
		if err = f.st.SettleTelegramDelete(t.Context(), a, true); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := f.st.ControlMessages(t.Context())
	if err != nil || len(rows) != 1 || rows[0].MessageID != 2000 {
		t.Fatal(rows, err)
	}
}
