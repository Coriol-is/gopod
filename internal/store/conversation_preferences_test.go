package store

import (
	"errors"
	"testing"
)

func preferenceStore(t *testing.T) *Store {
	t.Helper()
	s := newTestStore(t)
	if err := s.RegisterChat(t.Context(), RegisteredChat{JID: "tg:42", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return s
}
func selection(t *testing.T, s *Store, thread int) ConversationSelection {
	t.Helper()
	v, e := s.ResolveConversationSelection(t.Context(), "owner", thread, "claude")
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func changeSelection(t *testing.T, s *Store, thread int, kind, value string) ConversationSelection {
	t.Helper()
	v, e := s.ChangeConversationSelection(t.Context(), selection(t, s, thread), kind, value)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func TestConversationSelectionMigrationIsolation(t *testing.T) {
	s := preferenceStore(t)
	ctx := t.Context()
	if e := s.SetState(ctx, "provider:owner", "codex"); e != nil {
		t.Fatal(e)
	}
	if e := s.SetState(ctx, "model:owner", "gpt-5"); e != nil {
		t.Fatal(e)
	}
	for _, thread := range []int{0, 7, 8} {
		v := selection(t, s, thread)
		if v.Provider != "codex" || v.Model != "gpt-5" || !v.ProviderInherited || !v.ModelInherited {
			t.Fatalf("migration: %+v", v)
		}
	}
	changeSelection(t, s, 7, "provider", "claude")
	changeSelection(t, s, 7, "model", "sonnet")
	if v := selection(t, s, 8); v.Provider != "codex" || v.Model != "gpt-5" {
		t.Fatalf("leaked: %+v", v)
	}
	changeSelection(t, s, 7, "provider", "codex")
	if v := selection(t, s, 7); v.Model != "gpt-5" {
		t.Fatalf("foreign model: %+v", v)
	}
	changeSelection(t, s, 7, "provider", "claude")
	if v := selection(t, s, 7); v.Model != "sonnet" {
		t.Fatalf("lost model: %+v", v)
	}
	changeSelection(t, s, 0, "default_provider", "claude")
	if v := selection(t, s, 8); v.Provider != "claude" || v.Model != "" {
		t.Fatalf("default: %+v", v)
	}
	changeSelection(t, s, 0, "provider", "codex")
	if v := selection(t, s, 0); v.Provider != "codex" || v.ProviderInherited {
		t.Fatal(v)
	}
	changeSelection(t, s, 0, "provider", "")
	if v := selection(t, s, 0); v.Provider != "claude" || !v.ProviderInherited {
		t.Fatal(v)
	}
}
func TestConversationSelectionRejectsStaleAndPreservesClear(t *testing.T) {
	s := preferenceStore(t)
	old := selection(t, s, 7)
	changeSelection(t, s, 8, "model", "haiku")
	if _, e := s.ChangeConversationSelection(t.Context(), old, "provider", "codex"); !errors.Is(e, ErrSelectionChanged) {
		t.Fatalf("stale: %v", e)
	}
	changeSelection(t, s, 7, "provider", "codex")
	changeSelection(t, s, 7, "model", "gpt-5")
	if e := s.ClearConversationContinuity(t.Context(), "owner", 7); e != nil {
		t.Fatal(e)
	}
	if v := selection(t, s, 7); v.Provider != "codex" || v.Model != "gpt-5" {
		t.Fatal(v)
	}
}
func TestTurnSelectionIsImmutablePerAttempt(t *testing.T) {
	s := preferenceStore(t)
	ctx := t.Context()
	id, _, e := s.InsertTurn(ctx, Turn{Source: "telegram", SourceID: "42:1", ChatFolder: "owner", ChatID: 42, ThreadID: 7, Text: "hello"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkTurnRunning(ctx, id); e != nil {
		t.Fatal(e)
	}
	first := selection(t, s, 7)
	if _, e = s.BindTurnSelection(ctx, id, 1, first); e != nil {
		t.Fatal(e)
	}
	changed := changeSelection(t, s, 7, "provider", "codex")
	got, e := s.BindTurnSelection(ctx, id, 1, changed)
	if e != nil || got.Provider != "claude" {
		t.Fatalf("old attempt changed: %+v %v", got, e)
	}
	if e = s.MarkTurnRunning(ctx, id); e != nil {
		t.Fatal(e)
	}
	got, e = s.BindTurnSelection(ctx, id, 2, changed)
	if e != nil || got.Provider != "codex" {
		t.Fatalf("new attempt: %+v %v", got, e)
	}
	if _, e = s.BindTurnSelection(ctx, id, 1, changed); e == nil {
		t.Fatal("stale attempt admitted")
	}
}
func TestRetireConversationPreferencesIsScoped(t *testing.T) {
	s := preferenceStore(t)
	changeSelection(t, s, 7, "provider", "codex")
	changeSelection(t, s, 8, "model", "haiku")
	before := selection(t, s, 7)
	_, e := s.withTx(t.Context(), func(q taskSQL) error { return retireConversationPreferencesQ(t.Context(), q, before.Scope, 7) })
	if e != nil {
		t.Fatal(e)
	}
	if v := selection(t, s, 7); !v.ProviderInherited {
		t.Fatal(v)
	}
	if v := selection(t, s, 8); v.Model != "haiku" {
		t.Fatal(v)
	}
	if _, e = s.ChangeConversationSelection(t.Context(), before, "model", "old"); !errors.Is(e, ErrSelectionChanged) {
		t.Fatal(e)
	}
}

func TestConversationDefaultRemainsInheritedAndForeignSnapshotRejected(t *testing.T) {
	s := preferenceStore(t)
	ctx := t.Context()
	before := selection(t, s, 7)
	after, e := s.ResolveConversationSelection(ctx, "owner", 7, "codex")
	if e != nil || after.Provider != "codex" {
		t.Fatalf("global fallback pinned: %+v %v", after, e)
	}
	forged := before
	forged.Provider = "codex"
	if _, e = s.ChangeConversationSelection(ctx, forged, "model", "gpt-5"); !errors.Is(e, ErrSelectionChanged) {
		t.Fatalf("forged snapshot: %v", e)
	}
	changeSelection(t, s, 0, "provider", "codex")
	def, e := s.ResolveDefaultSelection(ctx, "owner", "claude")
	if e != nil || def.Provider != "claude" {
		t.Fatalf("main treated as wildcard: %+v %v", def, e)
	}
}
func TestMalformedConversationPreferencesFailClosed(t *testing.T) {
	s := preferenceStore(t)
	v := selection(t, s, 7)
	if _, e := s.db.ExecContext(t.Context(), `UPDATE conversation_preferences SET payload='{}' WHERE namespace_id=?`, v.Scope.NamespaceID); e != nil {
		t.Fatal(e)
	}
	if _, e := s.ResolveConversationSelection(t.Context(), "owner", 7, "claude"); e == nil {
		t.Fatal("malformed preferences accepted")
	}
}

func TestConversationSelectionRefusesConflictingDurableBinding(t *testing.T) {
	s := preferenceStore(t)
	ctx := t.Context()
	sel := selection(t, s, 7)
	id, _, e := s.InsertTurn(ctx, Turn{Source: "telegram", SourceID: "42:durable", ChatFolder: "owner", ChatID: 42, ThreadID: 7, Text: "safe"})
	if e != nil {
		t.Fatal(e)
	}
	if e = s.MarkTurnRunning(ctx, id); e != nil {
		t.Fatal(e)
	}
	binding, e := s.ResolveConversation(ctx, sel.Scope, 7)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.BindAttempt(ctx, sel.Scope, AttemptBinding{TurnID: id, SessionID: binding.ActiveSessionID, ContextEpoch: binding.ContextEpoch, Provider: "codex"}); e != nil {
		t.Fatal(e)
	}
	if _, e = s.BindTurnSelection(ctx, id, 1, sel); e == nil {
		t.Fatal("conflicting durable binding allowed")
	}
	if _, e = s.TurnSelection(ctx, id, 1); e == nil {
		t.Fatal("failed bind committed partial evidence")
	}
}
