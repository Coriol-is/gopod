package store

import "testing"

func TestKnownGroupNeedsObservedGroupMetadata(t *testing.T) {
	st := newTestStore(t)
	ctx := t.Context()
	if known, err := st.IsKnownGroup(ctx, "tg:-2"); err != nil || known {
		t.Fatal("negative ID inferred group")
	}
	for _, group := range []bool{true, false} {
		if err := st.UpsertChat(ctx, ChatRecord{JID: "tg:-2", IsGroup: group, LastMessageTime: 1}); err != nil {
			t.Fatal(err)
		}
		known, err := st.IsKnownGroup(ctx, "tg:-2")
		if err != nil || known != group {
			t.Fatalf("known=%v observed=%v err=%v", known, group, err)
		}
	}
}
