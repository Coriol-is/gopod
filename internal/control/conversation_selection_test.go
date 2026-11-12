package control

import (
	"context"
	"errors"
	"strconv"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func scopedControl(t *testing.T) (*Router, *store.Store, Caller) {
	t.Helper()
	st := testStore(t)
	if e := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:100", Folder: "owner", IsOwner: true, AddedAt: 1}); e != nil {
		t.Fatal(e)
	}
	r := New(silentLog())
	RegisterConversationSelectionCommands(r, ConversationSelectionDeps{Read: func(ctx context.Context, f string, thread int) (store.ConversationSelection, error) {
		return st.ResolveConversationSelection(ctx, f, thread, "claude")
	}, Change: st.ChangeConversationSelection, Folder: func(int64) string { return "owner" }, Providers: []string{"claude", "codex"}})
	return r, st, Caller{Source: SourceTelegram, ChatID: 100, ThreadID: 7, IsOwner: true}
}
func TestConversationProviderConfirmationScopeAndRevision(t *testing.T) {
	r, st, caller := scopedControl(t)
	cmd := Command{Name: "provider", Args: []string{"codex"}, Caller: caller}
	resp, e := r.Dispatch(t.Context(), cmd)
	if e != nil || resp.Code != CodeConfirm {
		t.Fatalf("confirmation: %+v %v", resp, e)
	}
	yes := resp.Buttons[0][0]
	cmd.Args = yes.Args
	cmd.Confirmed = true
	if got, e := r.Dispatch(t.Context(), cmd); e != nil || got.Code != 0 {
		t.Fatalf("confirm: %+v %v", got, e)
	}
	main, _ := st.ResolveConversationSelection(t.Context(), "owner", 0, "claude")
	topic, _ := st.ResolveConversationSelection(t.Context(), "owner", 7, "claude")
	if main.Provider != "claude" || topic.Provider != "codex" {
		t.Fatalf("scope: %+v %+v", main, topic)
	}
	if got, _ := r.Dispatch(t.Context(), cmd); got.Code == 0 {
		t.Fatal("stale button accepted")
	}
	cmd.Confirmed = false
	cmd.Args = []string{"inherit"}
	resp, _ = r.Dispatch(t.Context(), cmd)
	cmd.Args = resp.Buttons[0][0].Args
	cmd.Confirmed = true
	registered, _ := st.GetRegistered(t.Context(), "tg:100")
	registered.Name = "new registration revision"
	if e = st.RegisterChat(t.Context(), registered); e != nil {
		t.Fatal(e)
	}
	if got, _ := r.Dispatch(t.Context(), cmd); got.Code == 0 {
		t.Fatal("old registration accepted")
	}
}
func TestConversationModelProviderIsolationAndReset(t *testing.T) {
	r, st, caller := scopedControl(t)
	for _, name := range []string{"sonnet", "default", "inherit"} {
		resp, e := r.Dispatch(t.Context(), Command{Name: "model", Args: []string{name}, Caller: caller})
		if e != nil || resp.Code != 0 {
			t.Fatalf("model %s: %+v %v", name, resp, e)
		}
	}
	if resp, _ := r.Dispatch(t.Context(), Command{Name: "model", Args: []string{"gpt-5"}, Caller: caller}); resp.Code == 0 {
		t.Fatal("foreign provider model accepted")
	}
	v, _ := st.ResolveConversationSelection(t.Context(), "owner", 8, "claude")
	if v.Model != "" || !v.ModelInherited {
		t.Fatal(v)
	}
	caller.IsOwner = false
	caller.ChatID = -2
	if resp, e := r.Dispatch(t.Context(), Command{Name: "provider", Args: []string{"codex"}, Caller: caller}); e == nil && resp.Code == 0 {
		t.Fatal("group member changed provider")
	}
}
func TestScopedFallbackBindsAttemptRevisionAndKeepsOtherTopics(t *testing.T) {
	st, cmd := fallbackFixture(t)
	ctx := t.Context()
	id := mustID(cmd)
	if e := st.MarkTurnRunning(ctx, id); e != nil {
		t.Fatal(e)
	}
	selected, e := st.ResolveConversationSelection(ctx, "owner", 7, "claude")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.BindTurnSelection(ctx, id, 1, selected); e != nil {
		t.Fatal(e)
	}
	cmd.Args = append(cmd.Args, "1", strconv.FormatInt(selected.Revision, 10))
	r := New(silentLog())
	retried := 0
	RegisterConversationFallbackCommand(r, st, func(ctx context.Context, s store.ConversationSelection, target string) error {
		_, e := st.ChangeConversationSelection(ctx, s, "provider", target)
		if errors.Is(e, store.ErrSelectionChanged) {
			return ErrFallbackStale
		}
		return e
	}, func(context.Context, store.Turn) (bool, error) { retried++; return false, nil })
	resp, e := r.Dispatch(ctx, cmd)
	if e != nil || resp.Code != 0 || retried != 1 {
		t.Fatalf("fallback: %+v %v", resp, e)
	}
	other, _ := st.ResolveConversationSelection(ctx, "owner", 8, "claude")
	if other.Provider != "claude" {
		t.Fatal("fallback mutated other topic")
	}
	resp, e = r.Dispatch(ctx, cmd)
	if e != nil || resp.Code == 0 || retried != 1 {
		t.Fatalf("stale fallback: %+v %v", resp, e)
	}
}
