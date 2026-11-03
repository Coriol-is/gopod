package store

import (
	"errors"
	"fmt"
	"testing"
)

func TestOrdinaryRecoveryFrozenAndPublishedNeverReplay(t *testing.T) {
	for _, state := range []string{"unpublished", DeliveryPending, DeliveryPartial, DeliveryUncertain, DeliverySent, DeliveryFailed, DeliveryExpired, DeliveryCancelled} {
		t.Run(state, func(t *testing.T) {
			f := newDeliveryFixture(t)
			a := f.turn("telegram", "a", 0, 1)
			b := f.turn("telegram", "b", 0, 1)
			result := f.freeze(ExecInteractive, "one answer", ResultMember{a, 1}, ResultMember{b, 1})
			var delivery Delivery
			if state != "unpublished" {
				var err error
				delivery, err = f.st.PublishDelivery(t.Context(), f.publishReq(result.ID, "one answer"))
				if err != nil {
					t.Fatal(err)
				}
				// Simulate each durable state at controller loss. Recovery must neither
				// redraw the immutable plan nor change the dispatcher's ownership/state.
				_, err = f.st.db.ExecContext(t.Context(), `UPDATE delivery_groups SET state=? WHERE delivery_id=?`, state, delivery.ID)
				if err != nil {
					t.Fatal(err)
				}
			}
			for range 2 {
				for _, id := range []int64{a, b} {
					plan, err := f.st.RecoverOrdinaryTurn(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					tr, err := f.st.GetTurn(t.Context(), id)
					if err != nil {
						t.Fatal(err)
					}
					if tr.Attempts != 1 {
						t.Fatalf("attempt changed: %+v", tr)
					}
					if state == "unpublished" {
						if plan.Action != OrdinaryFrozen || plan.ResultID != result.ID || tr.Status != TurnRunning {
							t.Fatalf("lost frozen result: %+v %+v", plan, tr)
						}
					} else {
						if plan.Action != OrdinaryPublished && plan.Action != OrdinarySettled || tr.Status != TurnDone {
							t.Fatalf("publication not handed off: %+v %+v", plan, tr)
						}
						got, err := f.st.GetDelivery(t.Context(), delivery.ID)
						if err != nil || got.State != state || got.ResultID != result.ID {
							t.Fatalf("delivery changed: %+v %v", got, err)
						}
					}
				}
			}
			if got := countRows(t, f.st, `SELECT count(*) FROM turn_results`); got != 1 {
				t.Fatal(got)
			}
		})
	}
}

func TestOrdinaryRecoveryUsesAnyFrozenAttemptBeforeReplyMemo(t *testing.T) {
	f := newDeliveryFixture(t)
	id := f.turn("telegram", "a", 0, 1)
	result := f.freeze(ExecInteractive, "saved", ResultMember{id, 1})
	if err := f.st.MarkTurnRunning(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetTurnReply(t.Context(), id, 99); err != nil {
		t.Fatal(err)
	}
	plan, err := f.st.RecoverOrdinaryTurn(t.Context(), id)
	if err != nil || plan.Action != OrdinaryFrozen || plan.ResultID != result.ID {
		t.Fatal(plan, err)
	}
}

func TestOrdinaryRecoveryDoesNotSettleOnPublicationReadFailure(t *testing.T) {
	f := newDeliveryFixture(t)
	id := f.turn("telegram", "a", 0, 1)
	f.freeze(ExecInteractive, "saved", ResultMember{id, 1})
	// A failed ledger read cannot be interpreted as no result or completed work.
	if _, err := f.st.db.ExecContext(t.Context(), `ALTER TABLE delivery_groups RENAME TO unavailable_deliveries`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecoverOrdinaryTurn(t.Context(), id); err == nil {
		t.Fatal("accepted unreadable publication ledger")
	}
	tr, err := f.st.GetTurn(t.Context(), id)
	if err != nil || tr.Status != TurnRunning || tr.Attempts != 1 {
		t.Fatal(tr, err)
	}
}

func TestStartBatchFencesPartialResultAndTerminalMembersAtomically(t *testing.T) {
	for _, kind := range []string{"done", "failed", "stopped", "interrupted", "reply", "frozen", "published"} {
		t.Run(kind, func(t *testing.T) {
			f := newDeliveryFixture(t)
			a := f.turn("telegram", "a", 0, 0)
			b := f.turn("telegram", "b", 0, 1)
			switch kind {
			case "reply":
				if err := f.st.SetTurnReply(t.Context(), b, 99); err != nil {
					t.Fatal(err)
				}
			case "frozen", "published":
				result := f.freeze(ExecInteractive, "saved", ResultMember{b, 1})
				if kind == "published" {
					if _, err := f.st.PublishDelivery(t.Context(), f.publishReq(result.ID, "saved")); err != nil {
						t.Fatal(err)
					}
				}
			default:
				if err := f.st.MarkTurnFinished(t.Context(), b, kind, ""); err != nil {
					t.Fatal(err)
				}
			}
			batch := []TurnStart{{ID: a, Folder: "owner", Source: "telegram"}, {ID: b, Folder: "owner", Source: "telegram", Attempts: 1}}
			_, err := f.st.StartTurnBatch(t.Context(), batch)
			var hold *TurnBatchIneligibleError
			if !errors.As(err, &hold) || !hold.HoldBatch() {
				t.Fatalf("not a reconciliation hold: %v", err)
			}
			first, err := f.st.GetTurn(t.Context(), a)
			if err != nil || first.Status != TurnPending || first.Attempts != 0 {
				t.Fatalf("partial start: %+v %v", first, err)
			}
			second, err := f.st.GetTurn(t.Context(), b)
			if err != nil || second.Attempts != 1 {
				t.Fatal(second, err)
			}
		})
	}
}

func TestOrdinaryRecoveryLegacyUnknownAndAcknowledged(t *testing.T) {
	for _, reply := range []int{0, 33} {
		f := newDeliveryFixture(t)
		id := f.turn("telegram", fmt.Sprint(reply), 0, 1)
		if reply != 0 {
			if err := f.st.SetTurnReply(t.Context(), id, reply); err != nil {
				t.Fatal(err)
			}
		}
		plan, err := f.st.RecoverOrdinaryTurn(t.Context(), id)
		if err != nil || plan.Action != OrdinarySettled {
			t.Fatal(plan, err)
		}
		tr, _ := f.st.GetTurn(t.Context(), id)
		want := TurnFailed
		if reply != 0 {
			want = TurnDone
		}
		if tr.Status != want {
			t.Fatal(tr)
		}
	}
}

func TestInterruptedFrozenResultRemainsVisibleToBackupIdle(t *testing.T) {
	f := newDeliveryFixture(t)
	id := f.turn("telegram", "frozen", 0, 1)
	f.freeze(ExecInteractive, "saved", ResultMember{id, 1})
	if err := f.st.MarkTurnFinished(t.Context(), id, TurnInterrupted, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := f.st.RecoverOrdinaryTurn(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	evidence, err := ReadWorkEvidence(t.Context(), f.st.path)
	if err != nil || !evidence.Unsettled() || evidence.TurnsPending != 1 {
		t.Fatal(evidence, err)
	}
}

func TestOrdinaryRecoveryNeverStartedInterruptedCanDispatch(t *testing.T) {
	f := newDeliveryFixture(t)
	id := f.turn("telegram", "not-started", 0, 0)
	if err := f.st.MarkTurnFinished(t.Context(), id, TurnInterrupted, ""); err != nil {
		t.Fatal(err)
	}
	plan, err := f.st.RecoverOrdinaryTurn(t.Context(), id)
	if err != nil || plan.Action != OrdinaryDispatch {
		t.Fatal(plan, err)
	}
	attempts, err := f.st.StartTurnBatch(t.Context(), []TurnStart{{ID: id, Folder: "owner", Source: "telegram"}})
	if err != nil || len(attempts) != 1 || attempts[0] != 1 {
		t.Fatal(attempts, err)
	}
}
