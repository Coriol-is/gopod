package telegram

import (
	"context"
	"github.com/Coriol-is/gopod/internal/store"
	"strconv"
	"strings"
	"testing"
	"unicode/utf16"
)

func recoveryFixture(t *testing.T) (*Bot, *fakeTelegram, int64) {
	t.Helper()
	fake := editPathFake()
	b, it := newStreamFixture(t, fake)
	if err := b.store.RegisterChat(t.Context(), store.RegisteredChat{JID: buildChatJID(it.ChatID), Folder: it.Folder, Name: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	unregister := b.registerInflight(it.ID, it.ChatID, 0, func() {})
	b.recordPlaceholder(t.Context(), it, 77)
	if err := b.editMessageKB(t.Context(), it.ChatID, 77, "Partial answer", b.turnKeyboard(it.ID)); err != nil {
		t.Fatal(err)
	}
	unregister()
	return b, fake, it.ID
}

func TestControlRecoveryUnknownPreservesPartialAndClearsStop(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	if err := b.store.MarkTurnRunning(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := lastCall(t, fake, "editMessageText")
	if !strings.Contains(c.Form["text"], "Partial answer") || !strings.Contains(c.Form["text"], "unknown") || !cleared(t, c) {
		t.Fatalf("repair = %+v", c.Form)
	}
	rows, err := b.store.ControlMessages(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatal("retry should be delayed")
	}
}

func TestControlRecoveryTerminalFailedEditRetriesWithoutRegenerate(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	// A terminal display checkpoint exists even when the network call fails.
	if err := b.store.CheckpointControlMessage(t.Context(), streamChat, 77, "final answer", false, true); err != nil {
		t.Fatal(err)
	}
	if err := b.store.MarkTurnFinished(t.Context(), id, store.TurnDone, ""); err != nil {
		t.Fatal(err)
	}
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := lastCall(t, fake, "editMessageText")
	if c.Form["text"] != "final answer" || !cleared(t, c) {
		t.Fatalf("repair=%+v", c.Form)
	}
	rows, err := b.store.ControlMessages(t.Context())
	if err != nil || len(rows) != 0 {
		t.Fatalf("not acknowledged: %v %v", rows, err)
	}
}

func TestOldStopTokenCannotStopReusedTurn(t *testing.T) {
	b, _, id := recoveryFixture(t)
	old, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	defer b.registerInflight(id, streamChat, 0, cancel)()
	b.inflightMu.Lock()
	b.inflight[id].messages[77] = true
	b.inflightMu.Unlock()
	data, _ := encodeCB("st", strconv.FormatInt(id, 10), old.Token)
	decoded, _ := decodeCB(data)
	_, err = b.stopCallback(t.Context(), cbQuery{ChatID: streamChat, MessageID: 77, Data: decoded})
	if err != nil || ctx.Err() != nil {
		t.Fatalf("old callback cancelled retry: %v %v", err, ctx.Err())
	}
}

func TestControlRepairScopeRevocationDoesNotEdit(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	_ = id
	before := len(fake.callsTo("editMessageText"))
	// Change registration revision with the normal update path.
	rc, err := b.store.GetRegistered(t.Context(), buildChatJID(streamChat))
	if err != nil {
		t.Fatal(err)
	}
	rc.Name = "changed"
	if err = b.store.RegisterChat(t.Context(), rc); err != nil {
		t.Fatal(err)
	}
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("edited revoked scope")
	}
}

func TestControlRecoveryFailureRemainsPending(t *testing.T) {
	b, _, id := recoveryFixture(t)
	_ = id
	c, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	if err = b.store.FinishControlRepair(t.Context(), c, false, 0); err != nil {
		t.Fatal(err)
	}
	rows, err := b.store.ControlMessages(t.Context())
	if err != nil || len(rows) != 1 {
		t.Fatalf("lost pending repair: %v %v", rows, err)
	}
	if err = b.store.CheckpointControlMessage(t.Context(), streamChat, 77, "new partial", false, false); err != nil {
		t.Fatal(err)
	}
	if err = b.store.FinishControlRepair(t.Context(), c, true, 0); err != nil {
		t.Fatal(err)
	}
	next, _ := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if next.Terminal {
		t.Fatal("stale acknowledgement won")
	}
}

func TestTerminalControlRejectsLateProgress(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	unregister := b.registerInflight(id, streamChat, 0, func() {})
	kb := b.turnKeyboard(id)
	if err := b.editMessage(t.Context(), streamChat, 77, "Final answer"); err != nil {
		t.Fatal(err)
	}
	before := len(fake.callsTo("editMessageText"))
	if err := b.editMessageKB(t.Context(), streamChat, 77, "late chunk", kb); err != nil {
		t.Fatal(err)
	}
	unregister()
	if len(fake.callsTo("editMessageText")) != before {
		t.Fatal("late chunk overwrote terminal message")
	}
}

func TestRecoveryBoundsAstralPartial(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	if err := b.store.MarkTurnRunning(t.Context(), id); err != nil {
		t.Fatal(err)
	}
	if err := b.store.CheckpointControlMessage(t.Context(), streamChat, 77, strings.Repeat("😀", 3000), false, false); err != nil {
		t.Fatal(err)
	}
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	text := lastCall(t, fake, "editMessageText").Form["text"]
	if len(utf16.Encode([]rune(text))) > 4096 {
		t.Fatal("recovery text exceeds Telegram UTF16 limit")
	}
}

func TestControlEditFailurePersistsAcrossRecoverySweeps(t *testing.T) {
	b, fake, id := recoveryFixture(t)
	if err := b.store.MarkTurnFinished(t.Context(), id, store.TurnStopped, ""); err != nil {
		t.Fatal(err)
	}
	good := fake.respond
	fake.respond = func(method string, form map[string]string) (int, string) {
		if method == "editMessageText" {
			return apiError(429, "Too Many Requests: retry after 30")
		}
		return good(method, form)
	}
	if err := b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err := b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil {
		t.Fatal(err)
	}
	if c.Acknowledged != 0 || c.RetryAt == 0 {
		t.Fatalf("failed edit lost: %+v", c)
	}
	fake.respond = good
	if err = b.store.RequeueControlRepair(t.Context(), streamChat, 77, c.Token); err != nil {
		t.Fatal(err)
	}
	if err = b.RepairControls(t.Context()); err != nil {
		t.Fatal(err)
	}
	c, err = b.store.GetControlMessage(t.Context(), streamChat, 77)
	if err != nil || c.Acknowledged != c.Revision {
		t.Fatalf("repair did not converge: %+v %v", c, err)
	}
}
