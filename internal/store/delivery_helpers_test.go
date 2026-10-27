package store

import (
	"context"
	"testing"
)

// deliveryFixture is a registered personal chat plus helpers shared by the
// scope, result and outbox tests. Everything lives in a temporary database.
type deliveryFixture struct {
	t    *testing.T
	st   *Store
	conv ConversationScope
}

func newDeliveryFixture(t *testing.T) *deliveryFixture {
	t.Helper()
	st := newTestStore(t)
	ctx := t.Context()
	if err := st.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := st.SnapshotConversation(ctx, "tg:1", 0, VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	return &deliveryFixture{t: t, st: st, conv: conv}
}

func (f *deliveryFixture) turn(source, sourceID string, thread, attempts int) int64 {
	f.t.Helper()
	id, dup, err := f.st.InsertTurn(f.t.Context(), Turn{Source: source, SourceID: sourceID, ChatFolder: f.conv.Scope.Folder, ThreadID: thread, Text: "q", Attempts: attempts})
	if err != nil || dup {
		f.t.Fatalf("InsertTurn: %v dup=%v", err, dup)
	}
	for i := 0; i < attempts; i++ {
		if err = f.st.MarkTurnRunning(f.t.Context(), id); err != nil {
			f.t.Fatal(err)
		}
	}
	return id
}

func (f *deliveryFixture) freeze(kind ExecutionKind, text string, members ...ResultMember) TurnResult {
	f.t.Helper()
	res, err := f.st.FreezeTurnResult(f.t.Context(), ExecutionRef{Conversation: f.conv, Kind: kind, Members: members, Provider: "claude", Model: "m"}, text)
	if err != nil {
		f.t.Fatalf("FreezeTurnResult: %v", err)
	}
	return res
}

func textParts(texts ...string) []DeliveryPart {
	var out []DeliveryPart
	for _, s := range texts {
		out = append(out, DeliveryPart{Kind: PartText, Payload: []byte(s), ParseMode: "HTML"})
	}
	return out
}

func (f *deliveryFixture) publishReq(resultID string, texts ...string) PublishRequest {
	return PublishRequest{Conversation: f.conv, ResultID: resultID, Owner: OwnerControllerResult, Parts: textParts(texts...)}
}

func countRows(t *testing.T, st *Store, query string, args ...any) int {
	t.Helper()
	var n int
	if err := st.db.QueryRowContext(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
