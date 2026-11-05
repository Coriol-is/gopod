package main

import (
	"github.com/Coriol-is/gopod/internal/store"
	"io"
	"log/slog"
	"testing"
)

func TestReadyFinalRecoveryPublishesOnlyReady(t *testing.T) {
	r, _, _ := restartFixture(t)
	ctx := t.Context()
	conv, err := r.st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, source := range []string{"ready", "preparing", "stopped"} {
		id, _, e := r.st.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: source, ChatFolder: "owner"})
		if e != nil {
			t.Fatal(e)
		}
		if e = r.st.MarkTurnRunning(ctx, id); e != nil {
			t.Fatal(e)
		}
		result, e := r.st.FreezeTurnResult(ctx, store.ExecutionRef{Conversation: conv, Kind: store.ExecInteractive, Provider: "codex", Members: []store.ResultMember{{TurnID: id, Attempt: 1}}}, "exact source")
		if e != nil {
			t.Fatal(e)
		}
		if _, e = r.st.BeginFinalReply(ctx, result.ID, []byte(`{"mode":"text"}`)); e != nil {
			t.Fatal(e)
		}
		ids = append(ids, result.ID)
		if source != "preparing" {
			_, e = r.st.StageFinalReply(ctx, result.ID, store.FinalReplyPlan{Request: store.PublishRequest{ResultID: result.ID, Conversation: conv, Owner: store.OwnerControllerResult, Parts: []store.DeliveryPart{{Kind: store.PartText, Payload: []byte("exact final")}}}})
			if e != nil {
				t.Fatal(e)
			}
		}
		if source == "stopped" {
			if e = r.st.MarkTurnFinished(ctx, id, store.TurnStopped, "stopped"); e != nil {
				t.Fatal(e)
			}
		}
	}
	for range 2 {
		if err = recoverReadyFinalReplies(ctx, r.st, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
			t.Fatal(err)
		}
	}
	for i, id := range ids {
		intent, e := r.st.GetFinalReply(ctx, id)
		if e != nil {
			t.Fatal(e)
		}
		want := store.FinalReplyPublished
		if i == 1 {
			want = store.FinalReplyPreparing
		}
		if i == 2 {
			want = store.FinalReplyReady
		}
		if intent.State != want {
			t.Fatal(intent)
		}
	}
}
