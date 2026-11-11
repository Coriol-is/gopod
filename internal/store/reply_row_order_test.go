package store

import (
	"errors"
	"testing"
)

func legacySurface(t *testing.T, f *deliveryFixture, name string, msg int) int64 {
	t.Helper()
	id := f.turn("telegram", name, f.conv.ThreadID, 1)
	if _, err := f.st.db.Exec(`UPDATE turns SET chat_id=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReserveLegacyReplySurface(t.Context(), f.conv, id); err != nil {
		t.Fatal(err)
	}
	owner, err := f.st.BeginTelegramMessageOwner(t.Context(), 1, f.conv.ThreadID, f.conv.ThreadID)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AcknowledgeTelegramMessageOwner(t.Context(), owner, msg); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SetTurnReply(t.Context(), id, msg); err != nil {
		t.Fatal(err)
	}
	if err = f.st.RecordLegacyReplySurface(t.Context(), id, msg); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestReplySurfaceLateDurableReceiptCannotReorderLegacy(t *testing.T) {
	f, id, _, _, o := actionFixture(t) // durable presentation reserved first, no ACK yet
	legacy := legacySurface(t, f, "later legacy", 89)
	ackAction(t, o, 55) // late completion of earlier outbox, deliberately lower message ID
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Eligible || got.Newest {
		t.Fatal(got, err)
	}
	last, ok, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || !ok || last.TurnID != legacy || last.MessageID != 89 {
		t.Fatal(last, ok, err)
	}
	// Repeating the exact legacy receipt must preserve its sequence, not allocate.
	if err = f.st.RecordLegacyReplySurface(t.Context(), legacy, 89); err != nil {
		t.Fatal(err)
	}
	again, _, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || again != last {
		t.Fatal(again, err)
	}
	o.Release()
	path := f.st.path
	f.st.Close()
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	reopened, ok, err := st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || !ok || reopened != last {
		t.Fatal(reopened, ok, err)
	}
}

func TestReplySurfaceLateLegacyReceiptCannotReorderDurable(t *testing.T) {
	f := topicDeliveryFixture(t)
	legacy := f.turn("telegram", "earlier legacy", 7, 1)
	if _, err := f.st.db.Exec(`UPDATE turns SET chat_id=1 WHERE id=?`, legacy); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReserveLegacyReplySurface(t.Context(), f.conv, legacy); err != nil {
		t.Fatal(err)
	}
	id := f.turn("telegram", "later outbox", 7, 1)
	r := f.freeze(ExecInteractive, "answer", ResultMember{TurnID: id, Attempt: 1})
	req := f.publishReq(r.ID, "answer")
	req.Parts[0].ReplyMarkup = `{"inline_keyboard":[[{"text":"More","callback_data":"mo"}]]}`
	if _, err := f.st.PublishDelivery(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	o := f.owner()
	ackAction(t, o, 10)
	owner, err := f.st.BeginTelegramMessageOwner(t.Context(), 1, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AcknowledgeTelegramMessageOwner(t.Context(), owner, 999); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SetTurnReply(t.Context(), legacy, 999); err != nil {
		t.Fatal(err)
	}
	if err = f.st.RecordLegacyReplySurface(t.Context(), legacy, 999); err != nil {
		t.Fatal(err)
	}
	got, err := f.st.ReplyAction(t.Context(), id)
	if err != nil || !got.Newest {
		t.Fatal(got, err)
	}
	last, ok, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || !ok || last.MessageID != 10 || last.Source != "outbox" {
		t.Fatal(last, ok, err)
	}
}

func TestReplySurfaceUnknownHistoryAndPendingControlDoNotAdvance(t *testing.T) {
	f, c := repairAttemptFixture(t)
	if _, err := f.st.db.Exec(`UPDATE turns SET chat_id=1 WHERE id=?`, c.TurnID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetTurnReply(t.Context(), c.TurnID, c.MessageID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.ReserveLegacyReplySurface(t.Context(), f.conv, c.TurnID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordLegacyReplySurface(t.Context(), c.TurnID, c.MessageID); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	if err := f.st.CheckpointControlMessage(t.Context(), 1, c.MessageID, "final", false, true); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordLegacyReplySurface(t.Context(), c.TurnID, c.MessageID); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	if err := f.st.AcknowledgeControlEdit(t.Context(), 1, c.MessageID); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RecordLegacyReplySurface(t.Context(), c.TurnID, c.MessageID); err != nil {
		t.Fatal(err)
	}
	last, ok, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || !ok || last.MessageID != int64(c.MessageID) {
		t.Fatal(last, ok, err)
	}
	// A historical legacy send with no reservation cannot acquire chronology now.
	id := f.turn("telegram", "historical", 7, 1)
	if _, err := f.st.db.Exec(`UPDATE turns SET chat_id=1 WHERE id=?`, id); err != nil {
		t.Fatal(err)
	}
	owner, err := f.st.BeginTelegramMessageOwner(t.Context(), 1, 7, 7)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.AcknowledgeTelegramMessageOwner(t.Context(), owner, 88); err != nil {
		t.Fatal(err)
	}
	if err = f.st.SetTurnReply(t.Context(), id, 88); err != nil {
		t.Fatal(err)
	}
	if err = f.st.RecordLegacyReplySurface(t.Context(), id, 88); err != nil {
		t.Fatal(err)
	}
	after, _, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil || after != last {
		t.Fatal(after, err)
	}
}
