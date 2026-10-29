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
