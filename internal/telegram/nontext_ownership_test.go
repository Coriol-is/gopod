package telegram

import (
	"fmt"
	"testing"
)

func TestNontextCreationOwnershipOutcomes(t *testing.T) {
	for _, method := range []string{"sendDocument", "sendVoice"} {
		for _, outcome := range []string{"ack", "unknown", "wrong-receipt", "refused", "deleted", "receipt-failure"} {
			t.Run(method+"/"+outcome, func(t *testing.T) {
				fake := &fakeTelegram{}
				b, path, scope := ownershipFixture(t, fake)
				b.topicDeletionEnabled = true
				fake.respond = func(got string, form map[string]string) (int, string) {
					if got != method {
						return 200, `{"ok":true,"result":true}`
					}
					var count int
					if err := b.store.DB().QueryRow(`SELECT count(*) FROM telegram_message_ownership WHERE source_thread=7 AND target_thread=7 AND message_id IS NULL`).Scan(&count); err != nil || count != 1 {
						t.Errorf("missing precommit: %d %v", count, err)
					}
					switch outcome {
					case "wrong-receipt":
						return 200, `{"ok":true,"result":{"message_id":777,"chat":{"id":999,"type":"private"}}}`
					case "unknown":
						return apiError(500, "server error")
					case "refused":
						return apiError(400, "cannot send")
					case "deleted":
						return apiError(400, "Bad Request: TOPIC_DELETED")
					}
					return sentMessage("777")
				}
				if outcome == "receipt-failure" {
					if _, err := b.store.DB().Exec(`CREATE TRIGGER fail_nontext_receipt BEFORE UPDATE ON telegram_message_ownership BEGIN SELECT RAISE(ABORT,'receipt unavailable'); END`); err != nil {
						t.Fatal(err)
					}
				}
				var err error
				if method == "sendDocument" {
					_, err = b.sendDocument(t.Context(), streamChat, 7, "answer.txt", []byte("immutable content"), "caption")
				} else {
					err = b.sendVoiceReply(t.Context(), streamChat, 7, 0, []byte("voice"))
				}
				if outcome == "ack" && err != nil {
					t.Fatal(err)
				}
				if outcome != "ack" && err == nil {
					t.Fatal("missing error")
				}
				if (outcome == "unknown" || outcome == "wrong-receipt" || outcome == "deleted" || outcome == "receipt-failure") && !noReplayError(err) {
					t.Fatal("missing no replay", err)
				}
				calls := fake.callsTo(method)
				if len(calls) != 1 || calls[0].Form["message_thread_id"] != "7" {
					t.Fatal("rerouted or replayed", calls)
				}
				if outcome == "receipt-failure" {
					if _, e := b.store.DB().Exec(`DROP TRIGGER fail_nontext_receipt`); e != nil {
						t.Fatal(e)
					}
					if _, _, e := b.messageOwner(t.Context(), streamChat, 777); e != nil {
						t.Fatal(e)
					}
				}
				reopenOwnership(t, b, path)
				if outcome == "ack" || outcome == "receipt-failure" {
					owner, e := b.store.TelegramMessageOwner(t.Context(), streamChat, 777)
					if e != nil || owner.Scope != scope || owner.SourceThread != 7 || owner.TargetThread != 7 {
						t.Fatal(owner, e)
					}
				}
				var total, pending int
				if e := b.store.DB().QueryRow(`SELECT count(*),count(*) FILTER (WHERE message_id IS NULL) FROM telegram_message_ownership`).Scan(&total, &pending); e != nil {
					t.Fatal(e)
				}
				wantTotal, wantPending := 1, 0
				if outcome == "unknown" || outcome == "wrong-receipt" {
					wantPending = 1
				}
				if outcome == "refused" || outcome == "deleted" {
					wantTotal = 0
				}
				if total != wantTotal || pending != wantPending {
					t.Fatalf("rows total=%d pending=%d want=%d/%d", total, pending, wantTotal, wantPending)
				}

				if outcome == "deleted" {
					_, found, e := b.store.GetTopicDeletion(t.Context(), scope, 7)
					if e != nil || !found {
						t.Fatal(found, e)
					}
				}
				if len(fake.callsTo(method)) != 1 {
					t.Fatal("receipt recovery resent HTTP")
				}
			})
		}
	}
}

func TestDurableAsFileRecordsCreationOwnership(t *testing.T) {
	b, fake, item, _, owner := durableActionFixture(t)
	acknowledgeAction(t, owner, 700, false)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendDocument" {
			return sentMessage("777")
		}
		return 200, `{"ok":true,"result":true}`
	}
	if toast := pressed(t, b, fake, "file", 7, 700, fmt.Sprintf("af:%d", item.ID)); toast != "" {
		t.Fatal(toast)
	}
	got, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 777)
	if err != nil || got.SourceThread != 7 || got.TargetThread != 7 {
		t.Fatal(got, err)
	}
}

func TestNontextExistingFallbackPreservesSourceOwnership(t *testing.T) {
	for _, method := range []string{"sendDocument", "sendVoice"} {
		t.Run(method, func(t *testing.T) {
			fake := &fakeTelegram{respond: func(method string, _ map[string]string) (int, string) {
				if method == "sendDocument" || method == "sendVoice" {
					return sentMessage("778")
				}
				return 200, `{"ok":true,"result":true}`
			}}
			b, path, scope := ownershipFixture(t, fake)
			b.markThreadGone(streamChat, 7)
			var err error
			if method == "sendDocument" {
				_, err = b.sendDocument(t.Context(), streamChat, 7, "answer.txt", []byte("answer"), "")
			} else {
				err = b.sendVoiceReply(t.Context(), streamChat, 7, 0, []byte("voice"))
			}
			if err != nil {
				t.Fatal(err)
			}
			reopenOwnership(t, b, path)
			owner, err := b.store.TelegramMessageOwner(t.Context(), streamChat, 778)
			if err != nil || owner.Scope != scope || owner.SourceThread != 7 || owner.TargetThread != 0 {
				t.Fatal(owner, err)
			}
			if calls := fake.callsTo(method); len(calls) != 1 || calls[0].Form["message_thread_id"] != "" {
				t.Fatal(calls)
			}
		})
	}
}

func TestDurableAsFileUnknownKeepsPendingOwnership(t *testing.T) {
	b, fake, item, _, owner := durableActionFixture(t)
	acknowledgeAction(t, owner, 700, false)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendDocument" {
			return apiError(500, "unknown outcome")
		}
		return 200, `{"ok":true,"result":true}`
	}
	if toast := pressed(t, b, fake, "file", 7, 700, fmt.Sprintf("af:%d", item.ID)); toast != fileToastFailed {
		t.Fatal(toast)
	}
	if calls := fake.callsTo("sendDocument"); len(calls) != 1 || calls[0].Form["message_thread_id"] != "7" {
		t.Fatal(calls)
	}
	var count int
	if err := b.store.DB().QueryRow(`SELECT count(*) FROM telegram_message_ownership WHERE source_thread=7 AND target_thread=7 AND message_id IS NULL`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
}
