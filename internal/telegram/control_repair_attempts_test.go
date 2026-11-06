package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func TestRepairUnknownDelayedEffectBlocksFinalAcrossRestart(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	old, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	late, applied := make(chan struct{}), make(chan string, 1)
	fake.mu.Lock()
	fake.respond = func(_ string, p map[string]string) (int, string) {
		go func() { <-late; applied <- p["text"] }()
		return apiError(500, "delayed upstream")
	}
	fake.mu.Unlock()
	if err := b.repairControl(t.Context(), old); err == nil {
		t.Fatal("unknown not reported")
	}
	if err := b.store.ControlRepairBarrier(t.Context(), streamChat, 77); !errors.Is(err, store.ErrControlRepairPending) {
		t.Fatal(err)
	}
	_, owner := stageControlFinal(t, b, id)
	before := len(fake.callsTo("editMessageText"))
	for i := 0; i < 3; i++ {
		out := b.NewOutboxTransport(nil).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: streamChat, TargetMessageID: 77, Text: "final"})
		if out.Kind != OutcomeNotSent || out.Code != "repair_pending" {
			t.Fatal(out)
		}
		if err := b.editMessageKB(t.Context(), streamChat, 77, "late progress", nil); err == nil {
			t.Fatal("legacy bypass")
		}
		b.setKeyboard(t.Context(), streamChat, 77, noKeyboard())
		if err := b.RepairControls(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("conflicting HTTP sent")
	}
	// A delayed remote effect can now arrive; no final was sent to be overwritten.
	close(late)
	if text := <-applied; !strings.Contains(text, "Partial answer") {
		t.Fatal(text)
	}
	owner.Release()
	path := b.store.Path()
	b.store.Close()
	st, err := store.Open(t.Context(), path, b.log)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reopened := newFakeBot(t, fake)
	reopened.store = st
	if err = reopened.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := reopened.NewOutboxTransport(nil).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: streamChat, TargetMessageID: 77, Text: "final"})
	if out.Kind != OutcomeNotSent || len(fake.callsTo("editMessageText")) != before {
		t.Fatal(out)
	}
}
func TestRepairAckDatabaseFailureReconcilesWithoutHTTP(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_process", true: "restart"}[restart], func(t *testing.T) {
			b, fake, id := recoveryFixture(t)
			if err := b.store.MarkTurnFinished(t.Context(), id, store.TurnDone, ""); err != nil {
				t.Fatal(err)
			}
			c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
			if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_repair_settle BEFORE UPDATE OF state ON telegram_control_repair_attempts BEGIN SELECT RAISE(ABORT,'injected settle failure'); END`); err != nil {
				t.Fatal(err)
			}
			if err := b.repairControl(t.Context(), c); err == nil {
				t.Fatal("ACK failure hidden")
			}
			if err := b.store.ControlRepairBarrier(t.Context(), streamChat, 77); !errors.Is(err, store.ErrControlRepairPending) {
				t.Fatal(err)
			}
			before := len(fake.callsTo("editMessageText"))
			if _, err := b.store.DB().Exec(`DROP TRIGGER fail_repair_settle`); err != nil {
				t.Fatal(err)
			}
			if restart {
				path := b.store.Path()
				b.store.Close()
				st, err := store.Open(t.Context(), path, b.log)
				if err != nil {
					t.Fatal(err)
				}
				defer st.Close()
				b = newFakeBot(t, fake)
				b.store = st
			}
			if err := b.RepairControls(t.Context()); err != nil {
				t.Fatal(err)
			}
			if len(fake.callsTo("editMessageText")) != before {
				t.Fatal("receipt repair replayed HTTP")
			}
			err := b.store.ControlRepairBarrier(t.Context(), streamChat, 77)
			if restart && !errors.Is(err, store.ErrControlRepairPending) || !restart && err != nil {
				t.Fatal(err)
			}
			if !restart {
				got, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
				if !got.Terminal || got.Acknowledged != got.Revision {
					t.Fatal(got)
				}
			}
		})
	}
}
func TestRepairDefiniteRefusalRequiresSeparatePlainAttempt(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	if err := b.store.CheckpointControlMessage(t.Context(), streamChat, 77, "<b>final</b>", true, true); err != nil {
		t.Fatal(err)
	}
	if err := b.store.MarkTurnFinished(t.Context(), id, store.TurnDone, ""); err != nil {
		t.Fatal(err)
	}
	fake.mu.Lock()
	fake.respond = func(_ string, p map[string]string) (int, string) {
		if p["parse_mode"] == "HTML" {
			return apiError(400, "Bad Request: can't parse entities")
		}
		return sentMessage("77")
	}
	fake.mu.Unlock()
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	var refused, ack int
	if err := b.store.DB().QueryRow(`SELECT sum(state='refused'),sum(state='ack') FROM telegram_control_repair_attempts`).Scan(&refused, &ack); err != nil || refused != 1 || ack != 1 {
		t.Fatal(refused, ack, err)
	}
	if err := b.store.ControlRepairBarrier(t.Context(), streamChat, 77); err != nil {
		t.Fatal(err)
	}
}
func TestRepairPreHTTPFailureDoesNotJournalOrLatch(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	before := len(fake.callsTo("editMessageText"))
	if err := b.repairEdit(ctx, c, "x", "", false); err == nil {
		t.Fatal("cancel not reported")
	}
	var n int
	if err := b.store.DB().QueryRow(`SELECT count(*) FROM telegram_control_repair_attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("sent canceled request")
	}
}
func TestRepairMalformedSuccessStaysPending(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	fake.mu.Lock()
	fake.respond = func(string, map[string]string) (int, string) { return sentMessage("78") }
	fake.mu.Unlock()
	err := b.repairControl(t.Context(), c)
	if err == nil || !strings.Contains(err.Error(), "reconciliation") {
		t.Fatal(err)
	}
	if err = b.store.ControlRepairBarrier(t.Context(), streamChat, 77); !errors.Is(err, store.ErrControlRepairPending) {
		t.Fatal(err)
	}
}

func TestRepairReceiptFailureDoesNotBlockSiblingQuiescence(t *testing.T) {
	b, d := topicFenceFixture(t, &fakeTelegram{})
	b.repairReceipts = map[string]repairReceipt{"other": {attempt: store.ControlRepairAttempt{ID: "nonexistent", ChatID: streamChat, SourceThread: 8}, ack: true}}
	if err := b.QuiesceTopic(t.Context(), d); err != nil {
		t.Fatal("sibling receipt blocked exact topic", err)
	}
	if len(b.repairReceipts) != 1 {
		t.Fatal("sibling receipt lost")
	}
}

func TestRepairNotModifiedRequiresAttributedRefusal(t *testing.T) {
	for _, status := range []int{400, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
			fake.mu.Lock()
			fake.respond = func(string, map[string]string) (int, string) {
				return apiError(status, "Bad Request: message is not modified")
			}
			fake.mu.Unlock()
			err := b.repairEdit(t.Context(), c, "x", "", false)
			if status == 400 && err != nil || status == 500 && err == nil {
				t.Fatal(err)
			}
			held := b.store.ControlRepairBarrier(t.Context(), streamChat, 77)
			if status == 400 && held != nil || status == 500 && !errors.Is(held, store.ErrControlRepairPending) {
				t.Fatal(held)
			}
		})
	}
}
func TestRepairPrecommitFailureDoesNotSend(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_repair_begin BEFORE INSERT ON telegram_control_repair_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err := b.repairEdit(t.Context(), c, "x", "", false); err == nil {
		t.Fatal("commit failure hidden")
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("HTTP preceded commit")
	}
}
func TestRepairRefusalReceiptAndCapacityNeverReplay(t *testing.T) {
	for _, full := range []bool{false, true} {
		t.Run(fmt.Sprint(full), func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			c, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
			if full {
				b.repairReceipts = make(map[string]repairReceipt)
				for i := 0; i < messageReceiptLimit; i++ {
					id := fmt.Sprint(i)
					b.repairReceipts[id] = repairReceipt{attempt: store.ControlRepairAttempt{ID: id}}
				}
			}
			fake.mu.Lock()
			fake.respond = func(string, map[string]string) (int, string) {
				return apiError(400, "Bad Request: can't parse entities")
			}
			fake.mu.Unlock()
			if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_repair_settle BEFORE UPDATE OF state ON telegram_control_repair_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
				t.Fatal(err)
			}
			before := len(fake.callsTo("editMessageText"))
			err := b.repairEdit(t.Context(), c, "<b>x</b>", "HTML", false)
			if err == nil || isParseRefusal(err) {
				t.Fatal("unsettled refusal allowed fallback", err)
			}
			if _, err = b.store.DB().Exec(`DROP TRIGGER fail_repair_settle`); err != nil {
				t.Fatal(err)
			}
			if !full {
				if err = b.reconcileRepairReceipts(t.Context(), nil); err != nil {
					t.Fatal(err)
				}
				if err = b.store.ControlRepairBarrier(t.Context(), streamChat, 77); err != nil {
					t.Fatal(err)
				}
			} else {
				if len(b.repairReceipts) != messageReceiptLimit {
					t.Fatal("receipt eviction")
				}
				if err = b.store.ControlRepairBarrier(t.Context(), streamChat, 77); !errors.Is(err, store.ErrControlRepairPending) {
					t.Fatal(err)
				}
			}
			if len(fake.callsTo("editMessageText")) != before+1 {
				t.Fatal("receipt replayed HTTP")
			}
		})
	}
}
