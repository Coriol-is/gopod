package store

import (
	"errors"
	"testing"
)

func TestReplyRetirementExactAuthorityAndNoReplay(t *testing.T) {
	f, id, _, _, o := actionFixture(t)
	defer o.Release()
	ackAction(t, o, 55)
	legacySurface(t, f, "newer", 89)
	rows, err := f.st.ReplyRetirementCandidates(t.Context(), 1, 0)
	if err != nil || len(rows) != 1 || rows[0].TurnID != id {
		t.Fatal(rows, err)
	}
	row := rows[0]
	owner, covered, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 55)
	if err != nil || !covered {
		t.Fatal(err)
	}
	req := TelegramEditRequest{ChatID: 1, MessageID: 55, SourceThread: f.conv.ThreadID, Method: "markup", RequestHash: ReplyRetirementHash(1, 55), RetireSequence: row.Sequence}
	wrong := req
	wrong.Method = "text"
	if _, err = f.st.BeginTelegramEdit(t.Context(), wrong, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	wrong = req
	wrong.RequestHash = ReplyRetirementHash(1, 89)
	if _, err = f.st.BeginTelegramEdit(t.Context(), wrong, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	attempt, err := f.st.BeginTelegramEdit(t.Context(), req, owner, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal(err)
	}
	rows, err = f.st.ReplyRetirementCandidates(t.Context(), 1, 0)
	if err != nil || len(rows) != 0 {
		t.Fatal(rows, err)
	}
	if err = f.st.SettleTelegramEdit(t.Context(), attempt, false); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("refusal replay", err)
	}
}
func TestReplyRetirementRejectsCurrentAndReboundTarget(t *testing.T) {
	f := topicDeliveryFixture(t)
	id := legacySurface(t, f, "first", 55)
	first, _, err := f.st.LatestReplySurface(t.Context(), f.conv)
	if err != nil {
		t.Fatal(err)
	}
	owner, _, err := f.st.ResolveDirectEditOwner(t.Context(), 1, 55)
	if err != nil {
		t.Fatal(err)
	}
	req := TelegramEditRequest{ChatID: 1, MessageID: 55, SourceThread: 7, Method: "markup", RequestHash: ReplyRetirementHash(1, 55), RetireSequence: first.Sequence}
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("current row", err)
	}
	legacySurface(t, f, "second", 89)
	newer := f.turn("telegram", "rebound", 7, 1)
	_, err = f.st.db.Exec(`UPDATE turns SET chat_id=1,reply_msg_id=55 WHERE id=?`, newer)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.st.ReserveLegacyReplySurface(t.Context(), f.conv, newer); err != nil {
		t.Fatal(err)
	}
	if err = f.st.RecordLegacyReplySurface(t.Context(), newer, 55); err != nil {
		t.Fatal(err)
	}
	if _, err = f.st.BeginTelegramEdit(t.Context(), req, owner, false); !errors.Is(err, ErrTelegramEditHeld) {
		t.Fatal("rebound row", id, err)
	}
}
