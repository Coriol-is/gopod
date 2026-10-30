package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/proactive"
	"github.com/Coriol-is/gopod/internal/store"
)

func proactiveTestStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "s.sqlite"), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func TestProactiveServiceNeedsTheRegisteredOwnerChat(t *testing.T) {
	st := proactiveTestStore(t)
	resolve := proactiveService(st, 100)
	if _, err := resolve(t.Context()); err == nil {
		t.Fatal("an unregistered owner chat must make the feature unavailable, not guess a folder")
	}
	if err := st.RegisterChat(t.Context(), store.RegisteredChat{JID: "tg:100", Name: "owner", Folder: "owner-folder", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	svc, err := resolve(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err = svc.Enable(t.Context(), "gmail.morning"); err != nil {
		t.Fatal(err)
	}
	tk, err := st.GetTask(t.Context(), proactive.TaskID("gmail.morning"))
	if err != nil || tk.ChatFolder != "owner-folder" || tk.ChatJID != "tg:100" {
		t.Fatalf("task %+v %v", tk, err)
	}
}

func TestUpgradeProactiveTemplatesAtStartupIsQuietWithoutAnOwner(t *testing.T) {
	st := proactiveTestStore(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	upgradeProactiveTemplates(t.Context(), log, proactiveService(st, 100)) // no owner: no panic, nothing to do
	upgradeProactiveTemplates(t.Context(), log, proactiveService(st, 0))
}

func TestStartupUpgradeLeavesOtherOwnerTasksByteIdentical(t *testing.T) {
	ctx := t.Context()
	st := proactiveTestStore(t)
	if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:100", Name: "owner", Folder: "owner-folder", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	digest := store.TaskRecord{
		ID: "freefeed-digest", ChatFolder: "owner-folder", ChatJID: "tg:100", Prompt: "Post the digest",
		ScheduleType: "cron", ScheduleValue: "CRON_TZ=Europe/Belgrade 0 9 * * *", NextRun: 1893456000000, Status: "active", CreatedAt: 5,
	}
	if _, err := st.CreateTask(ctx, digest); err != nil {
		t.Fatal(err)
	}
	resolve := proactiveService(st, 100)
	svc, _ := resolve(ctx)
	if err := svc.Enable(ctx, "gmail.morning"); err != nil {
		t.Fatal(err)
	}
	// Make the preset look like it was written by an older template.
	row, _, _ := st.GetProactivePreset(ctx, "gmail.morning")
	row.TemplateVersion = 0
	if err := st.PutProactivePreset(ctx, row); err != nil {
		t.Fatal(err)
	}
	before, _ := st.GetTask(ctx, "freefeed-digest")

	upgradeProactiveTemplates(ctx, slog.New(slog.NewTextHandler(io.Discard, nil)), resolve)

	after, err := st.GetTask(ctx, "freefeed-digest")
	if err != nil || after != before {
		t.Fatalf("digest changed:\nbefore %+v\nafter  %+v\n%v", before, after, err)
	}
	row, _, _ = st.GetProactivePreset(ctx, "gmail.morning")
	if row.TemplateVersion == 0 {
		t.Fatal("the preset itself should have been upgraded")
	}
}
