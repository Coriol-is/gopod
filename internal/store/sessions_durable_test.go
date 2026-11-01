package store

import (
	"context"
	"errors"
	"testing"
)

// registerScope registers one chat and returns its current ScopeRef.
func registerScope(t *testing.T, st *Store, c RegisteredChat) ScopeRef {
	t.Helper()
	ctx := context.Background()
	if err := st.RegisterChat(ctx, c); err != nil {
		t.Fatalf("RegisterChat(%s): %v", c.Folder, err)
	}
	ref, err := st.SnapshotScope(ctx, c.Folder)
	if err != nil {
		t.Fatalf("SnapshotScope(%s): %v", c.Folder, err)
	}
	return ref
}

func ownerScope(t *testing.T, st *Store) ScopeRef {
	t.Helper()
	return registerScope(t, st, RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1})
}

// insertTestTurn inserts a terminal turn so foreign keys to turns(id) hold
// without making the controller look busy.
func insertTestTurn(t *testing.T, st *Store, folder, sourceID, status string) int64 {
	t.Helper()
	id, dup, err := st.InsertTurn(context.Background(), Turn{Source: "telegram", SourceID: sourceID, ChatFolder: folder, ChatID: 1, Text: "x", Status: status})
	if err != nil || dup {
		t.Fatalf("InsertTurn(%s): id=%d dup=%v err=%v", sourceID, id, dup, err)
	}
	return id
}

func TestResolveConversationCreatesOnceAndIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	first, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatalf("ResolveConversation: %v", err)
	}
	second, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatalf("ResolveConversation again: %v", err)
	}
	if first.ActiveSessionID == "" || first.ActiveSessionID != second.ActiveSessionID {
		t.Fatalf("session ids %q vs %q", first.ActiveSessionID, second.ActiveSessionID)
	}
	if first.ContextEpoch != 1 || first.Revision != 1 || first.Lifecycle != "active" {
		t.Fatalf("binding %+v", first)
	}
	if first.WorkspaceID != "legacy:owner" || first.NamespaceID != scope.NamespaceID || first.NamespaceEpoch != scope.NamespaceEpoch {
		t.Fatalf("binding %+v", first)
	}
	sess, err := st.GetExecutionSession(ctx, first.ActiveSessionID)
	if err != nil {
		t.Fatalf("GetExecutionSession: %v", err)
	}
	if sess.Kind != "conversation" || sess.Lifecycle != "active" || sess.Folder != "owner" || sess.RegistrationRevision != scope.RegistrationRevision {
		t.Fatalf("session %+v", sess)
	}
	n, _, err := st.CountSessionRows(ctx)
	if err != nil || n != 1 {
		t.Fatalf("CountSessionRows = %d, %v", n, err)
	}
}

func TestResolveConversationIsPerThread(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	a, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	b, err := st.ResolveConversation(ctx, scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	if a.ActiveSessionID == b.ActiveSessionID || b.ThreadID != 7 {
		t.Fatalf("threads share a session: %+v %+v", a, b)
	}
}

func TestResolveConversationRejectsStaleScope(t *testing.T) {
	st := openTestStore(t)
	scope := ownerScope(t, st)
	scope.RegistrationRevision++
	if _, err := st.ResolveConversation(context.Background(), scope, 0); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("err = %v, want ErrStaleScope", err)
	}
}

func TestResolveConversationRetiresOldEpoch(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	old := registerScope(t, st, RegisteredChat{JID: "tg:-2", Folder: "group-a", AddedAt: 2})
	first, err := st.ResolveConversation(ctx, old, 0)
	if err != nil {
		t.Fatal(err)
	}
	migrated, err := st.RotateGroupEpoch(ctx, "tg:-2", old.RegistrationRevision)
	if err != nil {
		t.Fatalf("RotateGroupEpoch: %v", err)
	}
	current, err := st.CompleteGroupEpoch(ctx, migrated.CurrentScope)
	if err != nil {
		t.Fatalf("CompleteGroupEpoch: %v", err)
	}
	next, err := st.ResolveConversation(ctx, current, 0)
	if err != nil {
		t.Fatalf("ResolveConversation new epoch: %v", err)
	}
	if next.ActiveSessionID == first.ActiveSessionID || next.NamespaceEpoch != 2 {
		t.Fatalf("new epoch reused session: %+v", next)
	}
	sess, err := st.GetExecutionSession(ctx, first.ActiveSessionID)
	if err != nil {
		t.Fatal(err)
	}
	if sess.Lifecycle != "retired" {
		t.Fatalf("old session lifecycle = %q, want retired", sess.Lifecycle)
	}
	var lifecycle string
	if err = st.db.QueryRowContext(ctx, `SELECT lifecycle FROM conversation_bindings WHERE namespace_id=? AND namespace_epoch=1 AND thread_id=0`, old.NamespaceID).Scan(&lifecycle); err != nil || lifecycle != "retired" {
		t.Fatalf("old binding lifecycle = %q, %v", lifecycle, err)
	}
	if _, err = st.ResolveConversation(ctx, old, 0); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("old scope err = %v, want ErrStaleScope", err)
	}
}

func TestNativeContextRoundTrip(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := b.ActiveSessionID
	if _, ok, err := st.GetNativeContext(ctx, sid, 1, "claude"); err != nil || ok {
		t.Fatalf("empty Get ok=%v err=%v", ok, err)
	}
	for provider, native := range map[string]string{"claude": "c-1", "codex": "x-1"} {
		if err = st.PutNativeContext(ctx, scope, sid, 1, provider, native); err != nil {
			t.Fatalf("Put %s: %v", provider, err)
		}
	}
	if err = st.PutNativeContext(ctx, scope, sid, 1, "claude", "c-2"); err != nil {
		t.Fatalf("Put replace: %v", err)
	}
	if got, ok, err := st.GetNativeContext(ctx, sid, 1, "claude"); err != nil || !ok || got != "c-2" {
		t.Fatalf("Get claude = %q %v %v", got, ok, err)
	}
	if got, ok, err := st.GetNativeContext(ctx, sid, 1, "codex"); err != nil || !ok || got != "x-1" {
		t.Fatalf("Get codex = %q %v %v", got, ok, err)
	}
	if err = st.DeleteNativeContext(ctx, scope, sid, 1, "claude"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok, err := st.GetNativeContext(ctx, sid, 1, "claude"); err != nil || ok {
		t.Fatalf("deleted Get ok=%v err=%v", ok, err)
	}
	if _, ok, _ := st.GetNativeContext(ctx, sid, 1, "codex"); !ok {
		t.Fatal("delete removed another provider")
	}
	if err = st.PutNativeContext(ctx, scope, sid, 2, "claude", "c-3"); !errors.Is(err, ErrStaleContextEpoch) {
		t.Fatalf("Put epoch 2 err = %v, want ErrStaleContextEpoch", err)
	}
	stale := scope
	stale.RegistrationRevision++
	if err = st.PutNativeContext(ctx, stale, sid, 1, "claude", "c-4"); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("Put stale scope err = %v, want ErrStaleScope", err)
	}
}

func TestBindAttemptIsIdempotentAndEpochChecked(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	a, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	other, err := st.ResolveConversation(ctx, scope, 7)
	if err != nil {
		t.Fatal(err)
	}
	turn := insertTestTurn(t, st, "owner", "1:1", TurnDone)
	bind := AttemptBinding{TurnID: turn, SessionID: a.ActiveSessionID, ContextEpoch: 1, Provider: "claude", Model: "m", ProfileFingerprint: "fp", RuntimeGeneration: "g1"}
	if err = st.BindAttempt(ctx, scope, bind); err != nil {
		t.Fatalf("BindAttempt: %v", err)
	}
	if err = st.BindAttempt(ctx, scope, bind); err != nil {
		t.Fatalf("BindAttempt repeat: %v", err)
	}
	got, ok, err := st.GetAttemptBinding(ctx, turn)
	if err != nil || !ok {
		t.Fatalf("GetAttemptBinding ok=%v err=%v", ok, err)
	}
	if got.SessionID != bind.SessionID || got.ContextEpoch != 1 || got.Provider != "claude" || got.Model != "m" || got.ProfileFingerprint != "fp" || got.RuntimeGeneration != "g1" || got.CreatedAt == 0 {
		t.Fatalf("binding %+v", got)
	}
	moved := bind
	moved.SessionID = other.ActiveSessionID
	if err = st.BindAttempt(ctx, scope, moved); !errors.Is(err, ErrAttemptBound) {
		t.Fatalf("rebind err = %v, want ErrAttemptBound", err)
	}
	stale := bind
	stale.TurnID = insertTestTurn(t, st, "owner", "1:2", TurnDone)
	stale.ContextEpoch = 2
	if err = st.BindAttempt(ctx, scope, stale); !errors.Is(err, ErrStaleContextEpoch) {
		t.Fatalf("epoch 2 err = %v, want ErrStaleContextEpoch", err)
	}
	if _, ok, err := st.GetAttemptBinding(ctx, stale.TurnID); err != nil || ok {
		t.Fatalf("stale bind persisted ok=%v err=%v", ok, err)
	}
}

func TestExecutionClaimIsUniquePerSession(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	sid := b.ActiveSessionID
	turnA := insertTestTurn(t, st, "owner", "1:1", TurnDone)
	turnB := insertTestTurn(t, st, "owner", "1:2", TurnDone)
	if err = st.AcquireExecutionClaim(ctx, scope, sid, turnA); err != nil {
		t.Fatalf("acquire A: %v", err)
	}
	if err = st.AcquireExecutionClaim(ctx, scope, sid, turnA); err != nil {
		t.Fatalf("acquire A again: %v", err)
	}
	if err = st.AcquireExecutionClaim(ctx, scope, sid, turnB); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("acquire B err = %v, want ErrSessionBusy", err)
	}
	if err = st.SettleExecutionClaim(ctx, sid, turnA, true); err != nil {
		t.Fatalf("settle unknown: %v", err)
	}
	var state string
	if err = st.db.QueryRowContext(ctx, `SELECT state FROM execution_claims WHERE session_id=?`, sid).Scan(&state); err != nil || state != "execution_unknown" {
		t.Fatalf("state = %q, %v", state, err)
	}
	if err = st.AcquireExecutionClaim(ctx, scope, sid, turnB); !errors.Is(err, ErrSessionBusy) {
		t.Fatalf("acquire B after unknown err = %v, want ErrSessionBusy", err)
	}
	if err = st.SettleExecutionClaim(ctx, sid, turnA, false); err != nil {
		t.Fatalf("settle: %v", err)
	}
	if err = st.AcquireExecutionClaim(ctx, scope, sid, turnB); err != nil {
		t.Fatalf("acquire B after settle: %v", err)
	}
}

func TestSessionSchemaConstraints(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	_, err := st.db.ExecContext(ctx, `INSERT INTO execution_sessions(session_id,namespace_id,namespace_epoch,registration_revision,folder,thread_id,kind,context_epoch,workspace_id,lifecycle,created_at) VALUES('s0',?,1,1,'owner',0,'conversation',0,'legacy:owner','active',1)`, scope.NamespaceID)
	if err == nil {
		t.Fatal("context_epoch 0 accepted")
	}
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = st.db.ExecContext(ctx, `INSERT INTO conversation_bindings(namespace_id,namespace_epoch,thread_id,active_session_id,lifecycle,updated_at) VALUES(?,?,0,?,'active',1)`, scope.NamespaceID, scope.NamespaceEpoch, b.ActiveSessionID)
	if err == nil {
		t.Fatal("duplicate binding primary key accepted")
	}
	_, err = st.db.ExecContext(ctx, `INSERT INTO conversation_bindings(namespace_id,namespace_epoch,thread_id,active_session_id,lifecycle,updated_at) VALUES(?,?,9,'missing','active',1)`, scope.NamespaceID, scope.NamespaceEpoch)
	if err == nil {
		t.Fatal("binding to a missing session accepted")
	}
}

func TestCountSessionRows(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err = st.PutNativeContext(ctx, scope, b.ActiveSessionID, 1, "claude", "c-1"); err != nil {
		t.Fatal(err)
	}
	if n, m, err := st.CountSessionRows(ctx); err != nil || n != 1 || m != 1 {
		t.Fatalf("CountSessionRows = %d, %d, %v", n, m, err)
	}
}

func TestBindAttemptRejectsForeignTurn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	registerScope(t, st, RegisteredChat{JID: "tg:-2", Folder: "group-b", AddedAt: 2})
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreign := insertTestTurn(t, st, "group-b", "2:1", TurnDone)
	err = st.BindAttempt(ctx, scope, AttemptBinding{TurnID: foreign, SessionID: b.ActiveSessionID, ContextEpoch: 1})
	if !errors.Is(err, ErrStaleScope) {
		t.Fatalf("err = %v, want ErrStaleScope", err)
	}
	if _, ok, err := st.GetAttemptBinding(ctx, foreign); err != nil || ok {
		t.Fatalf("foreign bind persisted ok=%v err=%v", ok, err)
	}
}

func TestExecutionClaimRejectsForeignTurn(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	registerScope(t, st, RegisteredChat{JID: "tg:-2", Folder: "group-b", AddedAt: 2})
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	foreign := insertTestTurn(t, st, "group-b", "2:1", TurnDone)
	if err = st.AcquireExecutionClaim(ctx, scope, b.ActiveSessionID, foreign); !errors.Is(err, ErrStaleScope) {
		t.Fatalf("err = %v, want ErrStaleScope", err)
	}
	var n int
	if err = st.db.QueryRowContext(ctx, `SELECT count(*) FROM execution_claims`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("execution_claims = %d, %v", n, err)
	}
}
