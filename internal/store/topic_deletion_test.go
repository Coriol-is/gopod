package store

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
)

func beginTopic(t *testing.T, s *Store, ref ScopeRef, thread int) TopicDeletion {
	t.Helper()
	d, created, err := s.BeginTopicDeletion(context.Background(), ref, thread)
	if err != nil || !created || d.IntentID == "" {
		t.Fatalf("begin: %+v %v %v", d, created, err)
	}
	return d
}

func TestTopicDeletionRefusalAndUncertainty(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ref := ownerScope(t, s)
	if err := s.PutSession(ctx, "owner", 7, "claude", "native"); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, s, ref, 7)
	again, created, err := s.BeginTopicDeletion(ctx, ref, 7)
	if err != nil || created || again.IntentID != d.IntentID {
		t.Fatalf("duplicate: %+v %v %v", again, created, err)
	}
	if _, err = s.ResolveConversation(ctx, ref, 7); !errors.Is(err, ErrTopicDeleting) {
		t.Fatalf("admission: %v", err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, d, "refused"); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveConversation(ctx, ref, 7); err != nil {
		t.Fatal(err)
	}
	if id, ok, err := s.GetSession(ctx, "owner", 7, "claude"); err != nil || !ok || id != "native" {
		t.Fatalf("refusal lost context %q %v %v", id, ok, err)
	}
	next := beginTopic(t, s, ref, 7)
	if next.IntentID == d.IntentID {
		t.Fatal("reused refused intent")
	}
	if err = s.RecordTopicDeletionOutcome(ctx, d, "deleted"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("stale result: %v", err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, next, "uncertain"); err != nil {
		t.Fatal(err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, next, "refused"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("uncertain restored admission: %v", err)
	}
	if err = s.RetireDeletedTopic(ctx, next); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("uncertain cleanup: %v", err)
	}
	if err = s.RecordTopicDeletionOutcome(ctx, next, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, next); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveConversation(ctx, ref, 7); !errors.Is(err, ErrConversationRetired) {
		t.Fatalf("retired admission: %v", err)
	}
}

func TestTopicDeletionRejectsWrongTargetsAndScopes(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ref := ownerScope(t, s)
	group := registerScope(t, s, RegisteredChat{JID: "tg:-2", Folder: "group", AddedAt: 1})
	other := registerScope(t, s, RegisteredChat{JID: "tg:3", Folder: "other", AddedAt: 1})
	for _, thread := range []int{0, -1} {
		if _, _, err := s.BeginTopicDeletion(ctx, ref, thread); !errors.Is(err, ErrInvalidConversation) {
			t.Fatalf("thread %d: %v", thread, err)
		}
	}
	if _, _, err := s.BeginTopicDeletion(ctx, group, 7); !errors.Is(err, ErrInvalidConversation) {
		t.Fatalf("group: %v", err)
	}
	stale := ref
	stale.RegistrationRevision++
	if _, _, err := s.BeginTopicDeletion(ctx, stale, 7); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("stale: %v", err)
	}
	d := beginTopic(t, s, ref, 7)
	forged := d
	forged.Scope = other
	if err := s.RecordTopicDeletionOutcome(ctx, forged, "deleted"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("foreign: %v", err)
	}
	forged = d
	forged.ThreadID = 8
	if err := s.RecordTopicDeletionOutcome(ctx, forged, "deleted"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("foreign thread: %v", err)
	}
	if err := s.RecordTopicDeletionOutcome(ctx, d, "closed"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("closed: %v", err)
	}
}

func TestTopicRetirementIsolationAndLateWrites(t *testing.T) {
	for _, mode := range []string{SessionMappingLegacy, SessionMappingDurable} {
		t.Run(mode, func(t *testing.T) {
			s := openTestStore(t)
			ctx := context.Background()
			ref := ownerScope(t, s)
			if err := s.SetState(ctx, "session_mapping", mode); err != nil {
				t.Fatal(err)
			}
			var target ConversationBinding
			for _, thread := range []int{0, 7, 8} {
				b, err := s.ResolveConversation(ctx, ref, thread)
				if err != nil {
					t.Fatal(err)
				}
				if thread == 7 {
					target = b
				}
				for _, p := range []string{"claude", "codex"} {
					if err = s.PutNativeContext(ctx, ref, b.ActiveSessionID, 1, p, "native"); err != nil {
						t.Fatal(err)
					}
					if err = s.PutSession(ctx, "owner", thread, p, "legacy"); err != nil {
						t.Fatal(err)
					}
				}
				if err = s.RecordConversationRequest(ctx, "owner", thread, "request"); err != nil {
					t.Fatal(err)
				}
				if err = s.CompleteConversation(ctx, "owner", thread, "claude", "answer"); err != nil {
					t.Fatal(err)
				}
			}
			d := beginTopic(t, s, ref, 7)
			if err := s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := s.RetireDeletedTopic(ctx, d); err != nil {
					t.Fatal(err)
				}
			}
			got, ok, err := s.GetTopicDeletion(ctx, ref, 7)
			if err != nil || !ok || got.State != "retired" {
				t.Fatalf("journal: %+v %v %v", got, ok, err)
			}
			for _, p := range []string{"claude", "codex"} {
				if _, ok, err := s.GetNativeContext(ctx, target.ActiveSessionID, 1, p); err != nil || ok {
					t.Fatalf("native remains: %v %v", ok, err)
				}
				if _, ok, err := s.GetSession(ctx, "owner", 7, p); err != nil || ok != (mode == SessionMappingDurable) {
					t.Fatalf("legacy freeze: %v %v", ok, err)
				}
			}
			for name, write := range map[string]func() error{
				"legacy":   func() error { return s.PutSession(ctx, "owner", 7, "claude", "late") },
				"native":   func() error { return s.PutNativeContext(ctx, ref, target.ActiveSessionID, 1, "claude", "late") },
				"request":  func() error { return s.RecordConversationRequest(ctx, "owner", 7, "late") },
				"complete": func() error { return s.CompleteConversation(ctx, "owner", 7, "claude", "late") },
				"handoff":  func() error { _, err := s.ConversationHandoff(ctx, "owner", 7, "codex"); return err },
				"turn": func() error {
					_, _, err := s.InsertTurn(ctx, Turn{Source: "telegram", SourceID: "late", ChatFolder: "owner", ThreadID: 7})
					return err
				},
			} {
				if err := write(); err == nil {
					t.Fatalf("%s resurrected topic", name)
				}
			}
			var n int
			if err := s.db.QueryRowContext(ctx, `SELECT count(*) FROM conversation_history WHERE chat_folder='owner' AND thread_id=7`).Scan(&n); err != nil || n != 0 {
				t.Fatalf("history %d %v", n, err)
			}
			for _, thread := range []int{0, 8} {
				if _, err := s.ResolveConversation(ctx, ref, thread); err != nil {
					t.Fatal(err)
				}
				if text, err := s.ConversationHandoff(ctx, "owner", thread, "codex"); err != nil || text == "" {
					t.Fatalf("other thread %d lost %q %v", thread, text, err)
				}
			}
		})
	}
}

func TestTopicRetirementWaitsForUnfinishedAndUnknownExecution(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ref := ownerScope(t, s)
	b, err := s.ResolveConversation(ctx, ref, 7)
	if err != nil {
		t.Fatal(err)
	}
	turn, _, err := s.InsertTurn(ctx, Turn{Source: "telegram", SourceID: "busy", ChatFolder: "owner", ThreadID: 7})
	if err != nil {
		t.Fatal(err)
	}
	if err = s.AcquireExecutionClaim(ctx, ref, b.ActiveSessionID, turn); err != nil {
		t.Fatal(err)
	}
	if err = s.PutNativeContext(ctx, ref, b.ActiveSessionID, 1, "claude", "keep"); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, s, ref, 7)
	if err = s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, d); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("busy: %v", err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE turns SET status='stopped' WHERE id=?`, turn); err != nil {
		t.Fatal(err)
	}
	if _, err = s.db.ExecContext(ctx, `UPDATE execution_claims SET state='execution_unknown' WHERE session_id=?`, b.ActiveSessionID); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, d); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("unknown: %v", err)
	}
	if _, ok, err := s.GetNativeContext(ctx, b.ActiveSessionID, 1, "claude"); err != nil || !ok {
		t.Fatalf("busy cleanup mutated context %v %v", ok, err)
	}
	if _, err = s.db.ExecContext(ctx, `DELETE FROM execution_claims WHERE session_id=?`, b.ActiveSessionID); err != nil {
		t.Fatal(err)
	}
	if err = s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
}

func TestTopicDeletionSurvivesReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "store.sqlite")
	s, err := Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	ref := ownerScope(t, s)
	d := beginTopic(t, s, ref, 7)
	if err = s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(ctx, path, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	got, created, err := s.BeginTopicDeletion(ctx, ref, 7)
	if err != nil || created || got.IntentID != d.IntentID || got.State != "deleted" {
		t.Fatalf("reopen: %+v %v %v", got, created, err)
	}
	if err = s.RetireDeletedTopic(ctx, got); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ResolveConversation(ctx, ref, 7); !errors.Is(err, ErrConversationRetired) {
		t.Fatalf("recreated absent binding: %v", err)
	}
}

func TestTopicRetirementRejectsLateFrozenResultAtomically(t *testing.T) {
	f := newDeliveryFixture(t)
	f.conv.ThreadID = 7
	turn := f.turn("telegram", "retired-freeze", 7, 1)
	if err := f.st.MarkTurnFinished(t.Context(), turn, TurnStopped, ""); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, f.st, f.conv.Scope, 7)
	if err := f.st.RecordTopicDeletionOutcome(t.Context(), d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.RetireDeletedTopic(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	_, err := f.st.FreezeTurnResult(t.Context(), ExecutionRef{Conversation: f.conv, Kind: ExecInteractive, Provider: "claude", Members: []ResultMember{{TurnID: turn, Attempt: 1}}}, "late answer")
	if !errors.Is(err, ErrConversationRetired) {
		t.Fatalf("late freeze: %v", err)
	}
	for _, table := range []string{"turn_results", "turn_result_members", "conversation_history"} {
		// Names are test literals, never database/user input.
		if n := countRows(t, f.st, "SELECT count(*) FROM "+table); n != 0 {
			t.Fatalf("%s partial write: %d", table, n)
		}
	}
}

func TestTopicRetirementCleanupRollback(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ref := ownerScope(t, s)
	if err := s.PutSession(ctx, "owner", 7, "claude", "keep"); err != nil {
		t.Fatal(err)
	}
	if err := s.RecordConversationRequest(ctx, "owner", 7, "keep"); err != nil {
		t.Fatal(err)
	}
	d := beginTopic(t, s, ref, 7)
	if err := s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.db.ExecContext(ctx, `CREATE TRIGGER fail_retirement BEFORE UPDATE OF state ON topic_deletions WHEN new.state='retired' BEGIN SELECT RAISE(ABORT,'injected cleanup fault'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireDeletedTopic(ctx, d); err == nil {
		t.Fatal("expected fault")
	}
	if n := countRows(t, s, `SELECT count(*) FROM conversation_history WHERE thread_id=7`); n != 1 {
		t.Fatalf("history partially cleared: %d", n)
	}
	if _, ok, err := s.GetSession(ctx, "owner", 7, "claude"); err != nil || !ok {
		t.Fatalf("session partially cleared: %v %v", ok, err)
	}
	got, _, err := s.GetTopicDeletion(ctx, ref, 7)
	if err != nil || got.State != "deleted" {
		t.Fatalf("cleanup not pending: %+v %v", got, err)
	}
	if _, err := s.db.ExecContext(ctx, `DROP TRIGGER fail_retirement`); err != nil {
		t.Fatal(err)
	}
	if err := s.RetireDeletedTopic(ctx, d); err != nil {
		t.Fatal(err)
	}
}

func TestTopicDeletionSerializesWithLegacySessionWrite(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	ref := ownerScope(t, s)
	for thread := 1; thread <= 20; thread++ {
		start := make(chan struct{})
		writes := make(chan error, 1)
		intents := make(chan TopicDeletion, 1)
		failures := make(chan error, 1)
		go func() { <-start; writes <- s.PutSession(ctx, "owner", thread, "claude", "racing") }()
		go func() { <-start; d, _, err := s.BeginTopicDeletion(ctx, ref, thread); intents <- d; failures <- err }()
		close(start)
		d := <-intents
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
		if err := <-writes; err != nil && !errors.Is(err, ErrTopicDeleting) {
			t.Fatal(err)
		}
		if err := s.RecordTopicDeletionOutcome(ctx, d, "deleted"); err != nil {
			t.Fatal(err)
		}
		if err := s.RetireDeletedTopic(ctx, d); err != nil {
			t.Fatal(err)
		}
		if _, ok, err := s.GetSession(ctx, "owner", thread, "claude"); err != nil || ok {
			t.Fatalf("racing write survived: %v %v", ok, err)
		}
	}
}

func TestTopicDeletionRegistrationChangeRequiresReconciliation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	old := ownerScope(t, s)
	d := beginTopic(t, s, old, 7)
	if err := s.RegisterChat(ctx, RegisteredChat{JID: "tg:1", Folder: "owner", Name: "renamed", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	current, err := s.SnapshotScope(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	if current.RegistrationRevision == old.RegistrationRevision {
		t.Fatal("revision did not advance")
	}
	if err = s.RecordTopicDeletionOutcome(ctx, d, "deleted"); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("old authority: %v", err)
	}
	forged := d
	forged.Scope = current
	if err = s.RecordTopicDeletionOutcome(ctx, forged, "deleted"); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatalf("transferred authority: %v", err)
	}
	if _, _, err = s.BeginTopicDeletion(ctx, current, 7); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("repeated API authority: %v", err)
	}
	if _, err = s.ResolveConversation(ctx, current, 7); !errors.Is(err, ErrTopicDeleting) {
		t.Fatalf("unsafe reopening: %v", err)
	}
}

func TestTopicDeletionDispatchClaimAndUnissuedRefusal(t *testing.T) {
	s := openTestStore(t)
	ref := ownerScope(t, s)
	d := beginTopic(t, s, ref, 7)
	if err := s.ClaimTopicDeletionDispatch(t.Context(), d); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimTopicDeletionDispatch(t.Context(), d); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal(err)
	}
	if err := s.RefuseUnissuedTopicDeletion(t.Context(), d); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal(err)
	}
	fresh := beginTopic(t, s, ref, 8)
	if err := s.RefuseUnissuedTopicDeletion(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimTopicDeletionDispatch(t.Context(), fresh); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal(err)
	}
}

func TestTopicDeletionOldSchemaNeverAcquiresDispatch(t *testing.T) {
	s := openTestStore(t)
	ref := ownerScope(t, s)
	d := beginTopic(t, s, ref, 7)
	if _, err := s.db.ExecContext(t.Context(), `ALTER TABLE topic_deletions DROP COLUMN dispatch_started`); err != nil {
		t.Fatal(err)
	}
	if err := s.applyTopicDeletionSchema(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.ClaimTopicDeletionDispatch(t.Context(), d); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal(err)
	}
	if err := s.RefuseUnissuedTopicDeletion(t.Context(), d); !errors.Is(err, ErrTopicDeletionState) {
		t.Fatal(err)
	}
}

func TestTopicDeletionBlocksBackupUntilSettled(t *testing.T) {
	s := openTestStore(t)
	ref := ownerScope(t, s)
	d := beginTopic(t, s, ref, 7)
	for _, state := range []string{"deleting", "uncertain", "deleted", "retired"} {
		if state == "retired" {
			if err := s.RetireDeletedTopic(t.Context(), d); err != nil {
				t.Fatal(err)
			}
		} else if state != "deleting" {
			if err := s.RecordTopicDeletionOutcome(t.Context(), d, state); err != nil {
				t.Fatal(err)
			}
		}
		ev, err := ReadWorkEvidence(t.Context(), s.path)
		want := int64(1)
		if state == "retired" {
			want = 0
		}
		if err != nil || ev.TopicDeletionsPending != want {
			t.Fatal(state, ev, err)
		}
	}
}
