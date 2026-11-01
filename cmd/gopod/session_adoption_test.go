package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func adoptionStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return st
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestAdoptSessionsIfRequestedNoopWithoutRequest(t *testing.T) {
	st := adoptionStore(t)
	ctx := context.Background()
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-1"); err != nil {
		t.Fatal(err)
	}
	if err := adoptSessionsIfRequested(ctx, st, discardLog()); err != nil {
		t.Fatal(err)
	}
	if mode, requested, err := st.SessionMappingMode(ctx); err != nil || mode != store.SessionMappingLegacy || requested {
		t.Fatalf("mode=%q requested=%v err=%v", mode, requested, err)
	}
	if n, m, err := st.CountSessionRows(ctx); err != nil || n != 0 || m != 0 {
		t.Fatalf("rows = %d, %d, %v", n, m, err)
	}
}

func TestAdoptSessionsIfRequestedFlipsMode(t *testing.T) {
	st := adoptionStore(t)
	ctx := context.Background()
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-1"); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adoptSessionsIfRequested(ctx, st, discardLog()); err != nil {
		t.Fatal(err)
	}
	if mode, _, err := st.SessionMappingMode(ctx); err != nil || mode != store.SessionMappingDurable {
		t.Fatalf("mode=%q err=%v", mode, err)
	}
	if n, m, err := st.CountSessionRows(ctx); err != nil || n != 1 || m != 1 {
		t.Fatalf("rows = %d, %d, %v", n, m, err)
	}
	// A second boot with the request still recorded is a no-op.
	if err := adoptSessionsIfRequested(ctx, st, discardLog()); err != nil {
		t.Fatal(err)
	}
}

func TestSessionsAdoptRefusedWhileBusy(t *testing.T) {
	st := adoptionStore(t)
	ctx := context.Background()
	if _, _, err := st.InsertTurn(ctx, store.Turn{Source: "telegram", SourceID: "1:1", ChatFolder: "owner", ChatID: 1, Text: "x"}); err != nil {
		t.Fatal(err)
	}
	if err := st.RequestSessionAdoption(ctx); err != nil {
		t.Fatal(err)
	}
	if err := adoptSessionsIfRequested(ctx, st, discardLog()); err != nil {
		t.Fatalf("busy adoption must not be fatal: %v", err)
	}
	mode, requested, err := st.SessionMappingMode(ctx)
	if err != nil || mode != store.SessionMappingLegacy || !requested {
		t.Fatalf("mode=%q requested=%v err=%v", mode, requested, err)
	}
}
