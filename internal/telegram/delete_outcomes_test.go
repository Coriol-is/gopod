package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestDeleteUnknownVoiceStopsFallbackAndPaidGeneration(t *testing.T) {
	for _, outcome := range []string{"false", "server", "malformed"} {
		t.Run(outcome, func(t *testing.T) {
			voiceOnly(t)
			t.Setenv("OPENAI_API_KEY", "fake")
			fake := editPathFake()
			inner := fake.respond
			fake.respond = func(method string, p map[string]string) (int, string) {
				if method == "deleteMessage" {
					switch outcome {
					case "false":
						return 200, `{"ok":true,"result":false}`
					case "malformed":
						return 200, `{"ok":true,"result":{"message_id":77}}`
					default:
						return apiError(500, "unknown deletion")
					}
				}
				return inner(method, p)
			}
			b, item := newStreamFixture(t, fake)
			registerPrivateFixture(t, b)
			paid := 0
			b.tts = func(context.Context, string, string) ([]byte, error) { paid++; return []byte("audio"), nil }
			b.syncRun = func(context.Context, string, int, runner.Tier, string) (string, error) { return "voice answer", nil }
			err := b.runAgentSync(t.Context(), item)
			if !noReplayError(err) || paid != 0 || len(fake.callsTo("sendVoice")) != 0 {
				t.Fatal(err, paid)
			}
			for _, call := range fake.callsTo("editMessageText") {
				if call.Form["text"] == "Finished." {
					t.Fatal("unknown fell through Finished")
				}
			}
			var n int
			if err = b.store.DB().QueryRow(`SELECT count(*) FROM telegram_delete_attempts WHERE state='pending'`).Scan(&n); err != nil || n != 1 {
				t.Fatal(n, err)
			}
		})
	}
}
func TestDeleteReceiptFailureReconcilesDatabaseOnly(t *testing.T) {
	for _, ack := range []bool{true, false} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			fake.respond = func(method string, p map[string]string) (int, string) {
				if method == "deleteMessage" {
					if ack {
						return 200, `{"ok":true,"result":true}`
					}
					return apiError(400, "can't delete")
				}
				return sentMessage(p["message_id"])
			}
			if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_delete_ack BEFORE UPDATE OF state ON telegram_delete_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
				t.Fatal(err)
			}
			if err := b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
				t.Fatal(err)
			}
			if len(fake.callsTo("deleteMessage")) != 1 {
				t.Fatal("wrong HTTP count")
			}
			if _, err := b.store.DB().Exec(`DROP TRIGGER fail_delete_ack`); err != nil {
				t.Fatal(err)
			}
			if err := b.reconcileDeleteReceipts(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if len(fake.callsTo("deleteMessage")) != 1 {
				t.Fatal("receipt retried HTTP")
			}
			c, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
			if ack {
				if err == nil {
					t.Fatal("control survived", c)
				}
				if err = b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
					t.Fatal(err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				if err = b.editMessage(t.Context(), streamChat, 77, "Finished."); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}
func TestDeleteRestartUnregisteredRawTargetBarrier(t *testing.T) {
	for _, ack := range []bool{true, false} {
		t.Run(fmt.Sprint(ack), func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			fake.respond = func(method string, p map[string]string) (int, string) {
				if method == "deleteMessage" {
					if ack {
						return 200, `{"ok":true,"result":true}`
					}
					return apiError(500, "unknown")
				}
				return sentMessage(p["message_id"])
			}
			err := b.deletePlaceholder(t.Context(), streamChat, 77)
			if ack && err != nil || !ack && !noReplayError(err) {
				t.Fatal(err)
			}
			if _, err = b.store.DB().Exec(`DELETE FROM registered_chats WHERE jid=?`, buildChatJID(streamChat)); err != nil {
				t.Fatal(err)
			}
			path := b.store.Path()
			b.store.Close()
			st, err := store.Open(t.Context(), path, b.log)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			b = newFakeBot(t, fake)
			b.store = st
			before := len(fake.callsTo("editMessageText"))
			if err = b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
				t.Fatal(err)
			}
			if err = b.editMessage(t.Context(), streamChat, 77, "late"); !noReplayError(err) {
				t.Fatal(err)
			}
			if len(fake.callsTo("deleteMessage")) != 1 || len(fake.callsTo("editMessageText")) != before {
				t.Fatal("restart/unregister bypassed barrier")
			}
			if err = b.topicRequest(t.Context(), streamChat, 8, func(context.Context) error { return nil }); err != nil {
				t.Fatal("sibling held", err)
			}
		})
	}
}
func TestDeleteCanceledBeforeAdmissionAndHistoricalTarget(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := b.deletePlaceholder(ctx, streamChat, 77); !errors.Is(err, context.Canceled) || !noReplayError(err) {
		t.Fatal(err)
	}
	if err := b.deletePlaceholder(t.Context(), streamChat, 999); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("deleteMessage")) != 0 {
		t.Fatal("undispatched calls sent")
	}
	var n int
	if err := b.store.DB().QueryRow(`SELECT count(*) FROM telegram_delete_attempts`).Scan(&n); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	b.topicMu.Lock()
	uncertain := len(b.topicUncertain)
	b.topicMu.Unlock()
	if uncertain != 0 {
		t.Fatal("local hold became remote uncertain")
	}
}
func TestDeletePrecommitFailureStopsHTTP(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_delete_insert BEFORE INSERT ON telegram_delete_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err := b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("deleteMessage")) != 0 {
		t.Fatal("failed precommit dispatched")
	}
}

func TestDeleteCancellationAfterDispatchRemainsPending(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	fake.respond = func(method string, p map[string]string) (int, string) {
		if method == "deleteMessage" {
			cancel()
			return apiError(500, "late remote effect possible")
		}
		return sentMessage(p["message_id"])
	}
	if err := b.deletePlaceholder(ctx, streamChat, 77); !noReplayError(err) {
		t.Fatal(err)
	}
	if len(fake.callsTo("deleteMessage")) != 1 {
		t.Fatal("did not dispatch")
	}
	var state string
	if err := b.store.DB().QueryRow(`SELECT state FROM telegram_delete_attempts`).Scan(&state); err != nil || state != "pending" {
		t.Fatal(state, err)
	}
	if err := b.editMessage(t.Context(), streamChat, 77, "late final"); !noReplayError(err) {
		t.Fatal(err)
	}
}
func TestDeleteReceiptLossRetainsPending(t *testing.T) {
	for _, overflow := range []bool{false, true} {
		t.Run(fmt.Sprint(overflow), func(t *testing.T) {
			b, fake, _ := recoveryFixture(t)
			fake.respond = func(method string, p map[string]string) (int, string) {
				if method == "deleteMessage" {
					return 200, `{"ok":true,"result":true}`
				}
				return sentMessage(p["message_id"])
			}
			if overflow {
				b.deleteReceipts = make(map[string]deleteReceipt)
				for i := 0; i < messageReceiptLimit; i++ {
					b.deleteReceipts[fmt.Sprint(i)] = deleteReceipt{}
				}
			}
			if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_delete_ack BEFORE UPDATE OF state ON telegram_delete_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
				t.Fatal(err)
			}
			if err := b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
				t.Fatal(err)
			}
			if overflow && len(b.deleteReceipts) != messageReceiptLimit {
				t.Fatal("receipt cap not bounded")
			}
			if _, err := b.store.DB().Exec(`DROP TRIGGER fail_delete_ack`); err != nil {
				t.Fatal(err)
			}
			path := b.store.Path()
			b.store.Close()
			st, err := store.Open(t.Context(), path, b.log)
			if err != nil {
				t.Fatal(err)
			}
			defer st.Close()
			b = newFakeBot(t, fake)
			b.store = st
			if err = b.reconcileDeleteReceipts(t.Context(), nil); err != nil {
				t.Fatal(err)
			}
			if err = b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
				t.Fatal(err)
			}
			if len(fake.callsTo("deleteMessage")) != 1 {
				t.Fatal("lost receipt replayed")
			}
			var state string
			if err = st.DB().QueryRow(`SELECT state FROM telegram_delete_attempts`).Scan(&state); err != nil || state != "pending" {
				t.Fatal(state, err)
			}
		})
	}
}

func TestDeleteFailedReceiptCannotStarveSiblingRepair(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	c, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	c.MessageID = 78
	c.Token = "sibling"
	if err = b.store.BindControlMessage(t.Context(), c); err != nil {
		t.Fatal(err)
	}
	if err = b.store.CheckpointControlMessage(t.Context(), streamChat, 78, "sibling final", false, true); err != nil {
		t.Fatal(err)
	}
	fake.respond = func(method string, p map[string]string) (int, string) {
		if method == "deleteMessage" {
			return 200, `{"ok":true,"result":true}`
		}
		return sentMessage(p["message_id"])
	}
	if _, err = b.store.DB().Exec(`CREATE TRIGGER fail_delete_ack BEFORE UPDATE OF state ON telegram_delete_attempts BEGIN SELECT RAISE(ABORT,'injected'); END`); err != nil {
		t.Fatal(err)
	}
	if err = b.deletePlaceholder(t.Context(), streamChat, 77); !noReplayError(err) {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("editMessageText")
	if len(calls) != before+1 || calls[len(calls)-1].Form["message_id"] != "78" {
		t.Fatal("sibling starved", calls)
	}
	if len(b.deleteReceipts) != 1 {
		t.Fatal("receipt lost")
	}
	if err = b.store.ControlRepairBarrier(t.Context(), streamChat, 77); !errors.Is(err, store.ErrTelegramDeleteHeld) {
		t.Fatal(err)
	}
}
