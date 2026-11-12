package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/queue"
	"github.com/Coriol-is/gopod/internal/store"
)

func durableActionFixture(t *testing.T) (*Bot, *fakeTelegram, queue.Item, store.Delivery, *store.OutboxOwner) {
	t.Helper()
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	item := turnIn(t, b, 7, 21)
	d := publishAction(t, b, item, "# Original source")
	owner, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(owner.Release)
	return b, fake, item, d, owner
}
func publishAction(t *testing.T, b *Bot, item queue.Item, text string) store.Delivery {
	t.Helper()
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(item.ChatID), item.ThreadID, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.MarkTurnRunning(t.Context(), item.ID); err != nil {
		t.Fatal(err)
	}
	r, err := b.store.FreezeTurnResult(t.Context(), store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "claude", Members: []store.ResultMember{{TurnID: item.ID, Attempt: 1}}}, text)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(replyKeyboard(item.ID, text))
	if err != nil {
		t.Fatal(err)
	}
	d, err := b.store.PublishDelivery(t.Context(), store.PublishRequest{ResultID: r.ID, Conversation: conv, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{{Kind: store.PartText, Payload: []byte("rendered output"), ReplyMarkup: string(raw)}}})
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func acknowledgeAction(t *testing.T, owner *store.OutboxOwner, msg int64, bare bool) {
	t.Helper()
	c, ok, err := owner.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if bare {
		c, err = owner.BeginKeyboardFallback(t.Context(), c)
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = owner.AckPart(t.Context(), c, msg); err != nil {
		t.Fatal(err)
	}
}
func TestDurableReplyActionsRecoverAfterRestart(t *testing.T) {
	b, fake, item, _, owner := durableActionFixture(t)
	acknowledgeAction(t, owner, 700, false)
	path := b.store.Path()
	owner.Release()
	if err := b.store.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), path, b.log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	b = newFakeBot(t, fake)
	b.ownerChatID = streamChat
	b.store = st
	b.registerCallbacks()
	for i, act := range []string{"mo", "mb", "af"} {
		if toast := pressed(t, b, fake, fmt.Sprint(i), 7, 700, fmt.Sprintf("%s:%d", act, item.ID)); toast != "" {
			t.Fatal(act, toast)
		}
	}
	edits := fake.callsTo("editMessageReplyMarkup")
	if len(edits) != 2 || !strings.Contains(edits[1].Form["reply_markup"], `"copy_text":{"text":"Original source"}`) {
		t.Fatal(edits)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 || docs[0].Form["document.content"] != "# Original source" || docs[0].Form["document.filename"] != "reply.md" || docs[0].Form["message_thread_id"] != "7" {
		t.Fatal(docs)
	}
	if len(fake.callsTo("sendMessage")) != 0 {
		t.Fatal("callback rerendered original reply")
	}
}
func TestDurableReplyActionsDenyMissingAuthorityDespiteMemo(t *testing.T) {
	for _, scenario := range []string{"pending", "bare", "wrong-message", "wrong-thread", "newer-durable", "mixed-legacy", "compacted", "stale-scope"} {
		t.Run(scenario, func(t *testing.T) {
			b, fake, item, _, owner := durableActionFixture(t)
			if scenario != "pending" {
				acknowledgeAction(t, owner, 700, scenario == "bare")
			}
			b.rememberTurn(item, func(m *turnMemo) { m.reply = "stale cache must never authorize" })
			thread, message := 7, 700
			switch scenario {
			case "wrong-message":
				message = 701
			case "wrong-thread":
				thread = 8
			case "newer-durable":
				newer := turnIn(t, b, 7, 22)
				publishAction(t, b, newer, "new reply")
				acknowledgeAction(t, owner, 800, false)
			case "mixed-legacy":
				b.rows.last = map[chatThread]lastReply{{streamChat, 7}: {msgID: 800, at: b.clock()}}
			case "compacted":
				if _, err := b.store.CompactTerminalDeliveries(t.Context(), time.Now().Add(31*24*time.Hour), 24*time.Hour, 1); err != nil {
					t.Fatal(err)
				}
			case "stale-scope":
				if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1, Name: "changed"}); err != nil {
					t.Fatal(err)
				}
			}
			for i, act := range []string{"mo", "mb", "af", "rg", "st2"} {
				if toast := pressed(t, b, fake, fmt.Sprint(i), thread, message, fmt.Sprintf("%s:%d", act, item.ID)); toast == "" {
					t.Fatal("accepted", act)
				}
			}
			if len(fake.callsTo("sendDocument"))+len(fake.callsTo("editMessageReplyMarkup"))+len(fake.callsTo("sendMessage")) != 0 {
				t.Fatal("denied callback caused side effects")
			}
		})
	}
}
func TestDurableReplyActionAsFileNeverRedirectsGoneTopic(t *testing.T) {
	b, fake, item, _, owner := durableActionFixture(t)
	acknowledgeAction(t, owner, 700, false)
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "sendDocument" {
			return apiError(400, "Bad Request: message thread not found")
		}
		return 200, `{"ok":true,"result":true}`
	}
	if toast := pressed(t, b, fake, "gone", 7, 700, fmt.Sprintf("af:%d", item.ID)); toast != fileToastFailed {
		t.Fatal(toast)
	}
	if calls := fake.callsTo("sendDocument"); len(calls) != 1 || calls[0].Form["message_thread_id"] != "7" {
		t.Fatal(calls)
	}
}

func TestDurableReplyActionCoalescedMemberCannotBorrowRow(t *testing.T) {
	fake := seqFake()
	b := newCallbackFixture(t, fake)
	b.registerCallbacks()
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(streamChat), Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	first, second := turnIn(t, b, 7, 31), turnIn(t, b, 7, 32)
	for _, it := range []queue.Item{first, second} {
		if err := b.store.MarkTurnRunning(t.Context(), it.ID); err != nil {
			t.Fatal(err)
		}
	}
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(streamChat), 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.store.FreezeTurnResult(t.Context(), store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "claude", Members: []store.ResultMember{{TurnID: first.ID, Attempt: 1}, {TurnID: second.ID, Attempt: 1}}}, "shared source")
	if err != nil {
		t.Fatal(err)
	}
	markup, err := json.Marshal(replyKeyboard(second.ID, "shared source"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = b.store.PublishDelivery(t.Context(), store.PublishRequest{Conversation: conv, ResultID: r.ID, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{{Kind: store.PartText, Payload: []byte("shared"), ReplyMarkup: string(markup)}}}); err != nil {
		t.Fatal(err)
	}
	o, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer o.Release()
	acknowledgeAction(t, o, 700, false)
	if toast := pressed(t, b, fake, "wrongmember", 7, 700, fmt.Sprintf("af:%d", first.ID)); toast == "" {
		t.Fatal("borrowed another member's row")
	}
	if toast := pressed(t, b, fake, "correctmember", 7, 700, fmt.Sprintf("af:%d", second.ID)); toast != "" {
		t.Fatal(toast)
	}
	docs := fake.callsTo("sendDocument")
	if len(docs) != 1 || docs[0].Form["document.content"] != "shared source" {
		t.Fatal(docs)
	}
}

func TestDurableReplyRecheckFailureIsKnownUnsent(t *testing.T) {
	b, _, item, _, owner := durableActionFixture(t)
	acknowledgeAction(t, owner, 700, false)
	turn, err := b.store.GetTurn(t.Context(), item.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := cbQuery{ChatID: streamChat, ThreadID: 7, MessageID: 700, Chat: store.RegisteredChat{Folder: "owner"}}
	view, toast, err := b.replyActionViewFor(t.Context(), q, turn)
	if err != nil || toast != "" || view.durable == nil {
		t.Fatal(view, toast, err)
	}
	next := turnIn(t, b, 7, 22)
	publishAction(t, b, next, "successor")
	acknowledgeAction(t, owner, 701, false)
	sent := false
	err = b.durableReplyRequest(t.Context(), q, turn, view, func(context.Context) error { sent = true; return nil })
	if err == nil || sent {
		t.Fatal("expired action dispatched", sent, err)
	}
	b.topicMu.Lock()
	uncertain := b.topicUncertain[chatThread{streamChat, 7}]
	b.topicMu.Unlock()
	if uncertain {
		t.Fatal("known-unsent callback latched topic uncertainty")
	}
}

func TestDurableReplyIgnoresOnlyProvenOlderLegacyCache(t *testing.T) {
	b, _, item, _, owner := durableActionFixture(t)
	// This fixture already reserved a durable publication. Create a later legacy
	// presentation, ACK it, then publish a still-later durable reply.
	legacy := turnIn(t, b, 7, 22)
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(item.ChatID), 7, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.ReserveLegacyReplySurface(t.Context(), conv, legacy.ID); err != nil {
		t.Fatal(err)
	}
	origin, err := b.store.BeginTelegramMessageOwner(t.Context(), item.ChatID, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.AcknowledgeTelegramMessageOwner(t.Context(), origin, 800); err != nil {
		t.Fatal(err)
	}
	if err = b.store.SetTurnReply(t.Context(), legacy.ID, 800); err != nil {
		t.Fatal(err)
	}
	if err = b.store.RecordLegacyReplySurface(t.Context(), legacy.ID, 800); err != nil {
		t.Fatal(err)
	}
	acknowledgeAction(t, owner, 700, false)
	newer := turnIn(t, b, 7, 23)
	publishAction(t, b, newer, "newest")
	acknowledgeAction(t, owner, 701, false)
	b.rows.last = map[chatThread]lastReply{{item.ChatID, 7}: {msgID: 800}}
	turn, err := b.store.GetTurn(t.Context(), newer.ID)
	if err != nil {
		t.Fatal(err)
	}
	q := cbQuery{ChatID: item.ChatID, ThreadID: 7, MessageID: 701, Chat: store.RegisteredChat{Folder: item.Folder}}
	_, toast, err := b.replyActionViewFor(t.Context(), q, turn)
	if err != nil || toast != "" {
		t.Fatal(toast, err)
	}
	// A truly unindexed row remains conservative; message ID comparison is not evidence.
	b.rows.last = map[chatThread]lastReply{{item.ChatID, 7}: {msgID: 2}}
	_, toast, err = b.replyActionViewFor(t.Context(), q, turn)
	if err != nil || toast != cbToastExpired {
		t.Fatal(toast, err)
	}
}
