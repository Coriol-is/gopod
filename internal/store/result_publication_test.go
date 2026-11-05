package store

import (
	"errors"
	"testing"
)

func TestStoppedReadyResultRemainsHeldAfterReopen(t *testing.T) {
	f, result, plan := finalFixture(t)
	prep := f.reserve(1, 10)
	f.ready(prep, 3)
	plan.Preparations = []string{prep.ID}
	plan.Request.Parts = append(plan.Request.Parts, DeliveryPart{Kind: PartUpload, ArtifactRef: ArtifactRef(prep.ArtifactIDs[0])})
	if _, err := f.st.StageFinalReply(t.Context(), result.ID, plan); err != nil {
		t.Fatal(err)
	}
	if err := f.st.MarkTurnFinished(t.Context(), result.Members[0].TurnID, TurnStopped, "stopped"); err != nil {
		t.Fatal(err)
	}
	path := f.st.path
	if err := f.st.Close(); err != nil {
		t.Fatal(err)
	}
	st, err := Open(t.Context(), path, silentLogger())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for range 2 {
		if _, err := st.PublishReadyFinalReply(t.Context(), result.ID); !errors.Is(err, ErrResultStopped) {
			t.Fatal(err)
		}
	}
	intent, err := st.GetFinalReply(t.Context(), result.ID)
	if err != nil || intent.State != FinalReplyReady || intent.DeliveryID != "" {
		t.Fatal(intent, err)
	}
	preserved, err := st.GetTurnResult(t.Context(), result.ID)
	if err != nil || preserved.Text != result.Text || preserved.TextHash != result.TextHash {
		t.Fatal(preserved, err)
	}
	if n := countRows(t, st, `SELECT count(*) FROM delivery_groups`); n != 0 {
		t.Fatal(n)
	}
	if n := countRows(t, st, `SELECT count(*) FROM artifact_refs WHERE ref_kind='outbox_part'`); n != 0 {
		t.Fatal(n)
	}
	reservation, err := readReservation(t.Context(), st.taskDB(t.Context()), prep.ID)
	if err != nil || reservation.state != "active" {
		t.Fatal(reservation, err)
	}
}

func TestStoppedMemberHoldsEntireInteractiveResult(t *testing.T) {
	f := newDeliveryFixture(t)
	one, two := f.turn("telegram", "one", 0, 1), f.turn("telegram", "two", 0, 1)
	result := f.freeze(ExecInteractive, "combined", ResultMember{TurnID: one, Attempt: 1}, ResultMember{TurnID: two, Attempt: 1})
	if err := f.st.MarkTurnFinished(t.Context(), two, TurnStopped, "stopped"); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.PublishDelivery(t.Context(), PublishRequest{ResultID: result.ID, Conversation: f.conv, Owner: OwnerControllerResult, Parts: []DeliveryPart{{Kind: PartText, Payload: []byte("combined")}}})
	if !errors.Is(err, ErrResultStopped) {
		t.Fatal(err)
	}
	var noReplay interface{ NoReplay() bool }
	if !errors.As(err, &noReplay) || !noReplay.NoReplay() {
		t.Fatal("hold permits replay")
	}
	if n := countRows(t, f.st, `SELECT count(*) FROM delivery_groups`); n != 0 {
		t.Fatal(n)
	}
}

func TestStoppedPublishedResultKeepsOutboxOwnership(t *testing.T) {
	f, result, plan := finalFixture(t)
	if _, err := f.st.StageFinalReply(t.Context(), result.ID, plan); err != nil {
		t.Fatal(err)
	}
	original, err := f.st.PublishReadyFinalReply(t.Context(), result.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.st.MarkTurnFinished(t.Context(), result.Members[0].TurnID, TurnStopped, "stopped"); err != nil {
		t.Fatal(err)
	}
	repeated, err := f.st.PublishReadyFinalReply(t.Context(), result.ID)
	if err != nil || repeated.ID != original.ID || repeated.State != original.State {
		t.Fatal(repeated, err)
	}
	// The general API must also return the existing immutable publication.
	plan.Request.ExpiresAt = original.ExpiresAt
	repeated, err = f.st.PublishDelivery(t.Context(), plan.Request)
	if err != nil || repeated.ID != original.ID {
		t.Fatal(repeated, err)
	}
}

func TestStoppedTaskResultRetainsManagedPublicationRules(t *testing.T) {
	f := newDeliveryFixture(t)
	id := f.turn("task", "task", 0, 1)
	result := f.freeze(ExecIsolatedTask, "task result", ResultMember{TurnID: id, Attempt: 1})
	if err := f.st.MarkTurnFinished(t.Context(), id, TurnStopped, "stopped"); err != nil {
		t.Fatal(err)
	}
	if err := checkResultPublicationQ(t.Context(), f.st.taskDB(t.Context()), result); err != nil {
		t.Fatal(err)
	}
}
