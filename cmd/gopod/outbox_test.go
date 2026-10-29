package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/ipc"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/Coriol-is/gopod/internal/telegram"
)

func TestAcquireOutboxOwnerWaitsForThePreviousInstance(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	held, err := st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	time.AfterFunc(80*time.Millisecond, held.Release)
	owner, err := acquireOutboxOwner(t.Context(), st, 3*time.Second, 10*time.Millisecond)
	if err != nil {
		t.Fatalf("did not acquire after release: %v", err)
	}
	owner.Release()

	held, err = st.AcquireOutboxOwner(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer held.Release()
	begin := time.Now()
	if _, err = acquireOutboxOwner(t.Context(), st, 100*time.Millisecond, 10*time.Millisecond); !errors.Is(err, store.ErrOutboxLocked) {
		t.Fatalf("err %v", err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Fatal("wait not bounded")
	}
}

func TestDurableIPCNeedsFlagDispatcherOwnerAndPersonalChat(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := durableIPC(true, true, st, "owner", 42); got.Store == nil || got.PersonalJID != "tg:42" {
		t.Fatalf("all gates open: %+v", got)
	}
	for name, got := range map[string]ipc.DurableConfig{
		"flag off":           durableIPC(false, true, st, "owner", 42),
		"dispatcher stopped": durableIPC(true, false, st, "owner", 42),
		"no owner folder":    durableIPC(true, true, st, "", 42),
		"no personal chat":   durableIPC(true, true, st, "owner", 0),
		"group id":           durableIPC(true, true, st, "owner", -100),
	} {
		if got.Store != nil || got.PersonalJID != "" {
			t.Errorf("%s: route enabled: %+v", name, got)
		}
	}
}

func TestArtifactIPCNeedsTheDurableRouteAndAPrivateStore(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(context.Background(), filepath.Join(dir, "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	cfg := config.Config{DataDir: dir, ChatsDir: filepath.Join(dir, "chats")}
	ac, blobs, err := openArtifactStore(cfg, st)
	if err != nil {
		t.Fatal(err)
	}
	defer blobs.Close()
	if fi, err := os.Stat(filepath.Join(dir, "artifacts")); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("artifact store dir: %v %v", fi, err)
	}
	if got := artifactIPC(durableIPC(true, true, st, "owner", 42), ac); got.Store == nil || got.Blobs == nil || got.Caps == nil {
		t.Fatalf("all gates open: %+v", got)
	}
	if got := artifactIPC(durableIPC(false, true, st, "owner", 42), ac); got.Store != nil {
		t.Fatalf("V1 enabled without the durable text route: %+v", got)
	}
	if got := artifactIPC(durableIPC(true, false, st, "owner", 42), ac); got.Store != nil {
		t.Fatalf("V1 enabled while the dispatcher is stopped: %+v", got)
	}
}

func TestPreparedDeliveryNeedsAnOwnerChat(t *testing.T) {
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if got := preparedDelivery(config.Config{}, st, nil, slog.Default()); got != nil {
		t.Fatal("prepared delivery without an owner chat")
	}
	got := preparedDelivery(config.Config{OwnerChatID: 42}, st, nil, slog.Default())
	if got == nil || got.Speech == nil || len(got.Speech.Policy.Grants) != 1 {
		t.Fatalf("got %+v", got)
	}
	g := got.Speech.Policy.Grants[0]
	if g.JID != "tg:42" || g.Backend != telegram.SpeechBackendOpenAI || !g.BilledCostAccepted || g.MaxChars != 4000 {
		t.Fatalf("grant %+v", g)
	}
}

func TestMaintainGenerationsSweepsAtStartupThenOnlyOldRows(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err = st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	conv, err := st.SnapshotConversation(ctx, "tg:1", 0, store.VisibilityConversation)
	if err != nil {
		t.Fatal(err)
	}
	g, _, err := st.BeginGeneration(ctx, conv.Scope, store.GenerationVoice, "k", "b")
	if err != nil {
		t.Fatal(err)
	}
	reconciled := make(chan struct{}, 4)
	done := make(chan struct{})
	go func() {
		maintainGenerations(ctx, st, 20*time.Millisecond, func(context.Context) { reconciled <- struct{}{} }, slog.New(slog.NewTextHandler(io.Discard, nil)))
		close(done)
	}()
	<-reconciled
	got, err := st.GetGeneration(ctx, g.ID)
	if err != nil || got.State != store.GenerationUnknown {
		t.Fatalf("generation after startup sweep = %+v, %v", got, err)
	}
	cancel()
	<-done
}
