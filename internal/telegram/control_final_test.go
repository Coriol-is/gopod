package telegram

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

func publishControlFinal(t *testing.T, b *Bot, id int64) (store.Delivery, *store.OutboxOwner, store.OutboxClaim) {
	d, o := stageControlFinal(t, b, id)
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	return d, o, c
}
func stageControlFinal(t *testing.T, b *Bot, id int64) (store.Delivery, *store.OutboxOwner) {
	t.Helper()
	if err := b.store.MarkTurnRunning(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(streamChat), 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	r, err := b.store.FreezeTurnResult(t.Context(), store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "claude", Members: []store.ResultMember{{TurnID: id, Attempt: 1}}}, "final source")
	if err != nil {
		t.Fatal(err)
	}
	kb, _ := json.Marshal(replyKeyboard(id, "final source"))
	d, err := b.store.PublishDelivery(t.Context(), store.PublishRequest{ResultID: r.ID, Conversation: conv, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{{Kind: store.PartEdit, TargetMessageID: 77, Payload: []byte("final rendered"), ReplyMarkup: string(kb)}}})
	if err != nil {
		t.Fatal(err)
	}
	o, err := b.store.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(o.Release)
	return d, o
}
func waitEditUsers(t *testing.T, b *Bot, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		b.controlMu.Lock()
		g := b.messageEdits[messageEditKey{streamChat, 77}]
		got := 0
		if g != nil {
			got = g.refs
		}
		b.controlMu.Unlock()
		if got == n {
			return
		}
		runtime.Gosched()
	}
	t.Fatal("edit waiter did not arrive")
}
func TestIssuedRepairFinishesBeforeFinalEdit(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	fake.mu.Lock()
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "editMessageText" && form["text"] != "final rendered" {
			close(entered)
			<-release
		}
		return sentMessage("77")
	}
	fake.mu.Unlock()
	repairDone := make(chan error, 1)
	go func() { repairDone <- b.RepairControls(t.Context()) }()
	<-entered
	_, o := stageControlFinal(t, b, id)
	finalDone := make(chan Outcome, 1)
	go func() {
		finalDone <- b.NewOutboxTransport(nil).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: streamChat, TargetMessageID: 77, Text: "final rendered"})
	}()
	waitEditUsers(t, b, 2)
	// An unrelated message remains independently writable while repair is blocked.
	free, err := b.lockMessageEdit(t.Context(), streamChat, 78)
	if err != nil {
		t.Fatal(err)
	}
	free()
	unblock()
	if err = <-repairDone; err != nil {
		t.Fatal(err)
	}
	if out := <-finalDone; out.Kind != OutcomeAck {
		t.Fatal(out)
	}
	c, ok, err := o.ClaimNext(t.Context(), time.Now())
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err = o.AckPart(t.Context(), c, 77); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before || lastCall(t, fake, "editMessageText").Form["text"] != "final rendered" {
		t.Fatal("repair overwrote final")
	}
}
func TestFinalEditBlocksStaleRepairBeforeAck(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	old, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _ = publishControlFinal(t, b, id)
	entered, release := make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	fake.mu.Lock()
	fake.respond = func(method string, _ map[string]string) (int, string) {
		if method == "editMessageText" {
			close(entered)
			<-release
		}
		return sentMessage("77")
	}
	fake.mu.Unlock()
	done := make(chan Outcome, 1)
	go func() {
		done <- b.NewOutboxTransport(nil).Attempt(t.Context(), SendRequest{Kind: store.PartEdit, ChatID: streamChat, TargetMessageID: 77, Text: "final rendered"})
	}()
	<-entered
	repair := make(chan error, 1)
	go func() { repair <- b.repairControl(t.Context(), old) }()
	waitEditUsers(t, b, 2)
	unblock()
	if out := <-done; out.Kind != OutcomeAck {
		t.Fatal(out)
	}
	if err = <-repair; err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != 2 {
		t.Fatal("repair dispatched before ACK")
	}
	// No DB ACK: replaying recovery after losing process-local gates still holds.
	b.messageEdits = nil
	if err = b.repairControl(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != 2 {
		t.Fatal("lost ACK allowed stale repair")
	}
}
func TestDurableMoreBackRetiresControlAndSurvivesObsoleteRepair(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	b.registerCallbacks()
	old, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	_, o, c := publishControlFinal(t, b, id)
	if err := o.AckPart(t.Context(), c, 77); err != nil {
		t.Fatal(err)
	}
	for _, act := range []string{"mo", "mb"} {
		if toast := pressed(t, b, fake, act, 0, 77, fmt.Sprintf("%s:%d", act, id)); toast != "" {
			t.Fatal(toast)
		}
		got, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
		if err != nil || !got.Terminal || got.Acknowledged != got.Revision || got.Revision <= old.Revision {
			t.Fatal(got, err)
		}
		if err = b.store.RequeueControlRepair(t.Context(), streamChat, 77, old.Token); err != nil {
			t.Fatal(err)
		}
		before := len(fake.callsTo("editMessageText"))
		if err = b.RepairControls(t.Context()); err != nil {
			t.Fatal(err)
		}
		if len(fake.callsTo("editMessageText")) != before {
			t.Fatal("obsolete Stop repair erased action row")
		}
	}
}
func TestMessageEditWaitingCancellationReleasesRegistry(t *testing.T) {
	b := &Bot{}
	release, err := b.lockMessageEdit(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() {
		free, e := b.lockMessageEdit(ctx, streamChat, 77)
		if free != nil {
			free()
		}
		done <- e
	}()
	waitEditUsers(t, b, 2)
	cancel()
	if e := <-done; e != context.Canceled {
		t.Fatal(e)
	}
	release()
	b.controlMu.Lock()
	defer b.controlMu.Unlock()
	if len(b.messageEdits) != 0 {
		t.Fatal("gate leaked")
	}
}

func TestPublishedFinalRejectsLateLegacyEdits(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	publishControlFinal(t, b, id)
	before := len(fake.callsTo("editMessageText"))
	if err := b.editMessageKB(t.Context(), streamChat, 77, "late partial", nil); err != nil {
		t.Fatal(err)
	}
	if err := b.editMessageHTMLKB(t.Context(), streamChat, 77, "late final", nil); err != nil {
		t.Fatal(err)
	}
	b.setKeyboard(t.Context(), streamChat, 77, noKeyboard())
	if len(fake.callsTo("editMessageText")) != before || len(fake.callsTo("editMessageReplyMarkup")) != 0 {
		t.Fatal("legacy writer touched durable target")
	}
}
func TestLegacyActionRequiresTerminalTextAcknowledgement(t *testing.T) {
	b, fake, _ := recoveryFixture(t)
	q := cbQuery{ChatID: streamChat, MessageID: 77}
	kb := moreKeyboard(1)
	if err := b.legacyReplyKeyboard(t.Context(), q, &kb); err == nil {
		t.Fatal("partial control granted action")
	}
	if err := b.store.CheckpointControlMessage(t.Context(), streamChat, 77, "final", false, true); err != nil {
		t.Fatal(err)
	}
	if err := b.legacyReplyKeyboard(t.Context(), q, &kb); err == nil {
		t.Fatal("unacknowledged final granted action")
	}
	if len(fake.callsTo("editMessageReplyMarkup")) != 0 {
		t.Fatal("invalid action sent")
	}
	if err := b.store.AcknowledgeControlEdit(t.Context(), streamChat, 77); err != nil {
		t.Fatal(err)
	}
	old, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err := b.legacyReplyKeyboard(t.Context(), q, &kb); err != nil {
		t.Fatal(err)
	}
	if err := b.store.RequeueControlRepair(t.Context(), streamChat, 77, old.Token); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := b.repairControl(t.Context(), old); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("repair overwrote acknowledged action")
	}
}

func TestHeldControlBatchDoesNotStarveUnrelatedRepair(t *testing.T) {
	b, fake, original := recoveryFixture(t)
	conv, err := b.store.SnapshotConversation(t.Context(), buildChatJID(streamChat), 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	messages := []int{77}
	for i := 0; i < 99; i++ {
		messages = append(messages, 100+i)
	}
	for batch := 0; batch < 4; batch++ {
		id := original
		if batch > 0 {
			id = turnIn(t, b, 0, int64(30+batch)).ID
		}
		if err = b.store.MarkTurnRunning(t.Context(), id); err != nil {
			t.Fatal(err)
		}
		r, e := b.store.FreezeTurnResult(t.Context(), store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "claude", Members: []store.ResultMember{{TurnID: id, Attempt: 1}}}, "final")
		if e != nil {
			t.Fatal(e)
		}
		var parts []store.DeliveryPart
		for _, msg := range messages[batch*25 : (batch+1)*25] {
			if msg != 77 {
				if err = b.store.BindControlMessage(t.Context(), store.ControlMessage{ChatID: streamChat, MessageID: msg, TurnID: id, Token: "old", Scope: conv.Scope}); err != nil {
					t.Fatal(err)
				}
			}
			parts = append(parts, store.DeliveryPart{Kind: store.PartEdit, TargetMessageID: int64(msg), Payload: []byte("final")})
		}
		if _, err = b.store.PublishDelivery(t.Context(), store.PublishRequest{ResultID: r.ID, Conversation: conv, Owner: store.OwnerControllerResult, Parts: parts}); err != nil {
			t.Fatal(err)
		}
	}
	if err = b.store.BindControlMessage(t.Context(), store.ControlMessage{ChatID: streamChat, MessageID: 1000, TurnID: original, Token: "other", Scope: conv.Scope}); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("held target edited")
	}
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	calls := fake.callsTo("editMessageText")
	if len(calls) != before+1 || calls[len(calls)-1].Form["message_id"] != "1000" {
		t.Fatal("unrelated row starved", len(calls), calls[len(calls)-1])
	}
}
