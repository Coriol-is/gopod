package store

import (
	"errors"
	"testing"
	"time"
)

func TestSettleUncertainEvidence(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "sent"}[sent], func(t *testing.T) {
			f := newDeliveryFixture(t)
			o := f.owner()
			d := f.publish("first", "tail")
			f.publish("next")
			c := mustClaim(t, o, time.Now())
			if err := o.MarkUncertain(t.Context(), c, "timeout", ""); err != nil {
				t.Fatal(err)
			}
			noClaim(t, o, time.Now())
			list, err := f.st.ListUncertainParts(t.Context())
			if err != nil || len(list) != 1 || list[0].PartID != c.PartID {
				t.Fatalf("list=%+v err=%v", list, err)
			}
			id := int64(0)
			if sent {
				id = 42
			}
			if err := f.st.SettleUncertain(t.Context(), c.PartID, id); err != nil {
				t.Fatal(err)
			}
			if err := f.st.SettleUncertain(t.Context(), c.PartID, id); !errors.Is(err, ErrClaimLost) {
				t.Fatalf("repeated settlement: %v", err)
			}
			diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
			if err != nil {
				t.Fatal(err)
			}
			if sent && diag.Parts[0].State != PartSent {
				t.Fatalf("%+v", diag)
			}
			if !sent && (diag.Parts[0].State != PartCancelled || diag.Parts[1].State != PartCancelled) {
				t.Fatalf("%+v", diag)
			}
			next := mustClaim(t, o, time.Now())
			want := "next"
			if sent {
				want = "tail"
			}
			if string(next.Part.Payload) != want {
				t.Fatalf("got %s want %s", next.Part.Payload, want)
			}
		})
	}
}

func TestExpiredUncertainDoesNotBlockLaterDelivery(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	req := f.publishReq("", "uncertain")
	req.ExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	d, err := f.st.PublishDelivery(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if err := o.MarkUncertain(t.Context(), c, "timeout", ""); err != nil {
		t.Fatal(err)
	}
	f.publish("next")
	next := mustClaim(t, o, time.UnixMilli(d.ExpiresAt).Add(time.Second))
	if string(next.Part.Payload) != "next" {
		t.Fatalf("replayed: %+v", next)
	}
	diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if err != nil || diag.Parts[0].State != PartUncertain {
		t.Fatalf("uncertainty lost: %+v %v", diag, err)
	}
}

func TestSettleRefusesSending(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	f.publish("in flight")
	c := mustClaim(t, o, time.Now())
	if err := f.st.SettleUncertain(t.Context(), c.PartID, 42); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("%v", err)
	}
}

func TestSettleUncertainEditRequiresExactTarget(t *testing.T) {
	f := newDeliveryFixture(t)
	o := f.owner()
	req := f.publishReq("", "edited answer", "tail")
	req.Parts[0].Kind = PartEdit
	req.Parts[0].TargetMessageID = 42
	d, err := f.st.PublishDelivery(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	c := mustClaim(t, o, time.Now())
	if err := o.MarkUncertain(t.Context(), c, "timeout", ""); err != nil {
		t.Fatal(err)
	}
	parts, err := f.st.ListUncertainParts(t.Context())
	if err != nil || len(parts) != 1 || parts[0].Kind != PartEdit || parts[0].TargetMessageID != 42 {
		t.Fatalf("missing edit evidence context: %+v %v", parts, err)
	}
	if err := f.st.SettleUncertain(t.Context(), c.PartID, 99); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("mismatched edit evidence accepted: %v", err)
	}
	diag, err := f.st.DeliveryDiagnostic(t.Context(), d.ID)
	if err != nil || diag.Parts[0].State != PartUncertain || diag.Parts[0].RemoteMessageID != 0 || diag.Parts[1].State != PartPending {
		t.Fatalf("invalid evidence mutated delivery: %+v %v", diag, err)
	}
	noClaim(t, o, time.Now())
	if err := f.st.SettleUncertain(t.Context(), c.PartID, 42); err != nil {
		t.Fatal(err)
	}
	next := mustClaim(t, o, time.Now())
	if string(next.Part.Payload) != "tail" {
		t.Fatalf("unexpected next part: %+v", next)
	}
}

func TestSettleUncertainEditPreservesReplyAuthority(t *testing.T) {
	for _, sent := range []bool{false, true} {
		t.Run(map[bool]string{false: "unsent", true: "sent"}[sent], func(t *testing.T) {
			f := newDeliveryFixture(t)
			o := f.owner()
			turnID := f.turn("telegram", "1:1", 0, 1)
			result := f.freeze(ExecInteractive, "answer", ResultMember{TurnID: turnID, Attempt: 1})
			req := f.publishReq(result.ID, "answer")
			req.Parts[0].Kind, req.Parts[0].TargetMessageID = PartEdit, 42
			if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
				t.Fatal(err)
			}
			c := mustClaim(t, o, time.Now())
			if err := o.MarkUncertain(t.Context(), c, "timeout", ""); err != nil {
				t.Fatal(err)
			}
			if err := f.st.SettleUncertain(t.Context(), c.PartID, 99); !errors.Is(err, ErrInvalidDelivery) {
				t.Fatal(err)
			}
			turn, err := f.st.GetTurn(t.Context(), turnID)
			if err != nil || turn.ReplyMsgID != 0 {
				t.Fatalf("false reply authority: %+v %v", turn, err)
			}
			id := int64(0)
			if sent {
				id = 42
			}
			if err := f.st.SettleUncertain(t.Context(), c.PartID, id); err != nil {
				t.Fatal(err)
			}
			turn, err = f.st.GetTurn(t.Context(), turnID)
			if err != nil || int64(turn.ReplyMsgID) != id {
				t.Fatalf("reply authority: %+v %v", turn, err)
			}
			noClaim(t, o, time.Now())
		})
	}
}
