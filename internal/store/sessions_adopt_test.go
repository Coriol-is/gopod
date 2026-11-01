package store

import (
	"context"
	"errors"
	"testing"
)

func TestSessionMappingDefaultsToLegacy(t *testing.T) {
	st := openTestStore(t)
	mode, requested, err := st.SessionMappingMode(context.Background())
	if err != nil || mode != SessionMappingLegacy || requested {
		t.Fatalf("mode=%q requested=%v err=%v", mode, requested, err)
	}
}

func TestAdoptLegacySessionsMapsEveryRow(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	a := registerScope(t, st, RegisteredChat{JID: "tg:1", Folder: "folder-a", IsOwner: true, AddedAt: 1})
	b := registerScope(t, st, RegisteredChat{JID: "tg:-2", Folder: "folder-b", AddedAt: 2})
	for _, r := range []struct {
		folder   string
		thread   int
		provider string
		id       string
	}{{"folder-a", 0, "claude", "legacy-a0"}, {"folder-a", 3, "codex", "legacy-a3"}, {"folder-b", 0, "claude", "legacy-b0"}} {
		if err := st.PutSession(ctx, r.folder, r.thread, r.provider, r.id); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatalf("RequestSessionAdoption: %v", err)
	}
	if _, requested, _ := st.SessionMappingMode(ctx); !requested {
		t.Fatal("request not recorded")
	}
	rep, err := st.AdoptLegacySessions(ctx)
	if err != nil {
		t.Fatalf("AdoptLegacySessions: %v", err)
	}
	if rep.Mode != SessionMappingDurable || rep.Sessions != 3 || rep.Contexts != 3 || rep.AlreadyDone {
		t.Fatalf("report %+v", rep)
	}
	for _, c := range []struct {
		scope    ScopeRef
		thread   int
		provider string
		want     string
	}{{a, 0, "claude", "legacy-a0"}, {a, 3, "codex", "legacy-a3"}, {b, 0, "claude", "legacy-b0"}} {
		bind, err := st.ResolveConversation(ctx, c.scope, c.thread)
		if err != nil {
			t.Fatal(err)
		}
		got, ok, err := st.GetNativeContext(ctx, bind.ActiveSessionID, 1, c.provider)
		if err != nil || !ok || got != c.want {
			t.Fatalf("%s/%d/%s = %q %v %v", c.scope.Folder, c.thread, c.provider, got, ok, err)
		}
	}
	mode, requested, err := st.SessionMappingMode(ctx)
	if err != nil || mode != SessionMappingDurable || requested {
		t.Fatalf("mode=%q requested=%v err=%v", mode, requested, err)
	}
}

func TestAdoptLegacySessionsSkipsUnregisteredRows(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	scope := ownerScope(t, st)
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-owner"); err != nil {
		t.Fatal(err)
	}
	if err := st.PutSession(ctx, "ghost", 0, "claude", "legacy-ghost"); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AdoptLegacySessions(ctx)
	if err != nil {
		t.Fatalf("AdoptLegacySessions: %v", err)
	}
	if rep.Mode != SessionMappingDurable || rep.Skipped != 1 || rep.Sessions != 1 || rep.Contexts != 1 {
		t.Fatalf("report %+v", rep)
	}
	if len(rep.SkippedFolders) != 1 || rep.SkippedFolders[0] != "ghost" {
		t.Fatalf("skipped folders %v", rep.SkippedFolders)
	}
	if mode, requested, _ := st.SessionMappingMode(ctx); mode != SessionMappingDurable || requested {
		t.Fatalf("mode = %q requested = %v", mode, requested)
	}
	b, err := st.ResolveConversation(ctx, scope, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got, ok, err := st.GetNativeContext(ctx, b.ActiveSessionID, 1, "claude"); err != nil || !ok || got != "legacy-owner" {
		t.Fatalf("owner context = %q %v %v", got, ok, err)
	}
}

func TestAdoptLegacySessionsIsAtomic(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ownerScope(t, st)
	registerScope(t, st, RegisteredChat{JID: "tg:-2", Folder: "zeta", AddedAt: 2})
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-owner"); err != nil {
		t.Fatal(err)
	}
	// Rows are copied in folder order, so the empty legacy id of "zeta"
	// violates the native_contexts CHECK after the owner row was already
	// copied; the whole adoption must roll back.
	if err := st.PutSession(ctx, "zeta", 0, "claude", ""); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdoptLegacySessions(ctx); err == nil {
		t.Fatal("adoption succeeded over an invalid legacy row")
	}
	if mode, requested, _ := st.SessionMappingMode(ctx); mode != SessionMappingLegacy || !requested {
		t.Fatalf("mode = %q requested = %v after failed adoption", mode, requested)
	}
	if n, m, err := st.CountSessionRows(ctx); err != nil || n != 0 || m != 0 {
		t.Fatalf("execution_sessions = %d native_contexts = %d, %v", n, m, err)
	}
}

func TestCancelSessionAdoptionClearsRequest(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatal(err)
	}
	if err := st.CancelSessionAdoption(ctx); err != nil {
		t.Fatalf("CancelSessionAdoption: %v", err)
	}
	if mode, requested, err := st.SessionMappingMode(ctx); err != nil || mode != SessionMappingLegacy || requested {
		t.Fatalf("mode=%q requested=%v err=%v", mode, requested, err)
	}
}

func TestAdoptLegacySessionsRefusedWhileBusy(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ownerScope(t, st)
	insertTestTurn(t, st, "owner", "1:1", TurnPending)
	if _, err := st.AdoptLegacySessions(ctx); !errors.Is(err, ErrAdoptionBusy) {
		t.Fatalf("err = %v, want ErrAdoptionBusy", err)
	}
	if mode, _, _ := st.SessionMappingMode(ctx); mode != SessionMappingLegacy {
		t.Fatalf("mode = %q", mode)
	}
}

func TestAdoptLegacySessionsIsIdempotent(t *testing.T) {
	st := openTestStore(t)
	ctx := context.Background()
	ownerScope(t, st)
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.AdoptLegacySessions(ctx); err != nil {
		t.Fatal(err)
	}
	rep, err := st.AdoptLegacySessions(ctx)
	if err != nil {
		t.Fatalf("second adoption: %v", err)
	}
	if !rep.AlreadyDone || rep.Mode != SessionMappingDurable {
		t.Fatalf("report %+v", rep)
	}
	if n, m, err := st.CountSessionRows(ctx); err != nil || n != 1 || m != 1 {
		t.Fatalf("counts %d %d %v", n, m, err)
	}
}
