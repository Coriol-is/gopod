package store

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestTopicRetirementAtomicallyRemovesOnlyItsPreferences(t *testing.T) {
	s := preferenceStore(t)
	ctx := t.Context()
	changeSelection(t, s, 0, "provider", "codex")
	changeSelection(t, s, 7, "provider", "codex")
	changeSelection(t, s, 7, "model", "gpt-5")
	changeSelection(t, s, 8, "model", "haiku")
	before := selection(t, s, 7)
	if err := s.PutSession(ctx, "owner", 7, "codex", "preserved-until-retirement"); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, s, before.Scope, 7)
	if err := s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
		t.Fatal(err)
	}
	read := func() (conversationPreferences, int64) {
		t.Helper()
		var raw string
		var revision int64
		if err := s.db.QueryRowContext(ctx, `SELECT payload,revision FROM conversation_preferences WHERE namespace_id=? AND namespace_epoch=?`, before.Scope.NamespaceID, before.Scope.NamespaceEpoch).Scan(&raw, &revision); err != nil {
			t.Fatal(err)
		}
		var prefs conversationPreferences
		if err := json.Unmarshal([]byte(raw), &prefs); err != nil {
			t.Fatal(err)
		}
		return prefs, revision
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_retirement BEFORE UPDATE OF state ON topic_deletions WHEN NEW.state='retired' BEGIN SELECT RAISE(ABORT,'injected retirement failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireDeletedTopic(ctx, d); err == nil {
		t.Fatal("injected transaction failure ignored")
	}
	prefs, rev := read()
	if prefs.Providers[7] != "codex" || prefs.ConversationModels[7]["codex"] != "gpt-5" || rev != before.Revision {
		t.Fatal("failed retirement changed preferences")
	}
	if _, found, err := s.GetSession(ctx, "owner", 7, "codex"); err != nil || !found {
		t.Fatalf("failed retirement lost session: %v", err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_retirement`); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
	prefs, rev = read()
	if _, exists := prefs.Providers[7]; exists {
		t.Fatal("retired provider override remains")
	}
	if _, exists := prefs.ConversationModels[7]; exists {
		t.Fatal("retired models remain")
	}
	if prefs.Providers[0] != "codex" || prefs.ConversationModels[8]["claude"] != "haiku" || rev != before.Revision+1 {
		t.Fatal("retirement changed sibling preferences or revision")
	}
	if _, err := s.ResolveConversationSelection(ctx, "owner", 7, "claude"); !errors.Is(err, ErrConversationRetired) {
		t.Fatalf("retired topic resolved preferences: %v", err)
	}
	if err := s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
	_, again := read()
	if again != rev {
		t.Fatal("idempotent retirement changed preference revision")
	}
}
