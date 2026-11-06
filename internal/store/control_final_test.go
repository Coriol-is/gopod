package store

import (
	"testing"
	"time"
)

func TestControlFinalOwnershipPersistsAcrossOutcomes(t *testing.T) {
	for _, state := range []string{"pending", "sending", "uncertain", "failed", "cancelled", "expired", "sent", "compacted"} {
		t.Run(state, func(t *testing.T) {
			f := topicDeliveryFixture(t)
			id := f.turn("telegram", "control-final", 7, 1)
			c := ControlMessage{ChatID: 1, MessageID: 77, TurnID: id, Token: "old", Scope: f.conv.Scope, ThreadID: 7}
			if err := f.st.BindControlMessage(t.Context(), c); err != nil {
				t.Fatal(err)
			}
			c, _ = f.st.GetControlMessage(t.Context(), 1, 77)
			r := f.freeze(ExecInteractive, "final", ResultMember{TurnID: id, Attempt: 1})
			req := f.publishReq(r.ID, "final")
			req.Parts[0].Kind = PartEdit
			req.Parts[0].TargetMessageID = 77
			d, err := f.st.PublishDelivery(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			if state == "sent" || state == "compacted" {
				o := f.owner()
				ackAction(t, o, 77)
				o.Release()
				if state == "compacted" {
					if _, err = f.st.CompactTerminalDeliveries(t.Context(), time.Now().Add(48*time.Hour), time.Hour, 10); err != nil {
						t.Fatal(err)
					}
				}
			} else if state != "pending" {
				if _, err = f.st.db.Exec(`UPDATE outbox_parts SET state=? WHERE delivery_id=?`, state, d.ID); err != nil {
					t.Fatal(err)
				}
			}
			path := f.st.path
			if err = f.st.Close(); err != nil {
				t.Fatal(err)
			}
			st, err := Open(t.Context(), path, silentLogger())
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			held, err := st.ControlFinalOwnership(t.Context(), c)
			if err != nil || !held {
				t.Fatal(held, err)
			}
			got, _ := st.GetControlMessage(t.Context(), 1, 77)
			ack := state == "sent" || state == "compacted"
			if got.Terminal != ack || ack && (got.Acknowledged != got.Revision || got.Revision <= c.Revision) {
				t.Fatal(got)
			}
			wrong := c
			wrong.Scope.NamespaceEpoch++
			held, err = st.ControlFinalOwnership(t.Context(), wrong)
			if err != nil || held {
				t.Fatal("cross scope", held, err)
			}
		})
	}
}

func TestControlFinalRetirementDoesNotAcknowledgeNewerRevision(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	c := ControlMessage{ChatID: 1, MessageID: 55, TurnID: id, Token: "old", Scope: f.conv.Scope, ThreadID: 7}
	if err := f.st.BindControlMessage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	c, _ = f.st.GetControlMessage(t.Context(), 1, 55)
	ackAction(t, o, 55)
	if err := f.st.CheckpointControlMessage(t.Context(), 1, 55, "new checkpoint", false, false); err != nil {
		t.Fatal(err)
	}
	held, err := f.st.ControlFinalOwnership(t.Context(), c)
	if err != nil || !held {
		t.Fatal(held, err)
	}
	got, _ := f.st.GetControlMessage(t.Context(), 1, 55)
	if got.Terminal || got.Acknowledged != 0 {
		t.Fatal("retired successor", got)
	}
}
