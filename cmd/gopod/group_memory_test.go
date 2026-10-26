package main

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/memory"
	"github.com/Coriol-is/gopod/internal/store"
)

const adapterOwnerID int64 = 1001

type adapterEmbedder struct {
	calls     int
	forbidden bool
}

func (e *adapterEmbedder) Model() string {
	if e.forbidden {
		panic("local group path accessed personal embedder model")
	}
	return "adapter-test"
}
func (e *adapterEmbedder) Dim() int {
	if e.forbidden {
		panic("local group path accessed personal embedder dimension")
	}
	return 1024
}
func (e *adapterEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	if e.forbidden {
		panic("group adapter invoked personal embedding service")
	}
	e.calls++
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, 1024)
		out[i][0] = 1
	}
	return out, nil
}

type groupMemoryFixture struct {
	st          *store.Store
	mem         *memory.Memory
	embedder    *adapterEmbedder
	groupFolder string
}

func newGroupMemoryFixture(t *testing.T) groupMemoryFixture {
	t.Helper()
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "memory.sqlite"), logger)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, c := range []store.RegisteredChat{
		{JID: "tg:1001", Folder: "personal", IsOwner: true, AddedAt: time.Now().UnixMilli()},
		{JID: "tg:-2002", Folder: "group-legacy", AddedAt: time.Now().UnixMilli()},
		{JID: "tg:1002", Folder: "forged-private-owner", IsOwner: true, AddedAt: time.Now().UnixMilli()},
		{JID: "tg:-2003", Folder: "forged-group-owner", IsOwner: true, AddedAt: time.Now().UnixMilli()},
	} {
		if err = st.RegisterChat(ctx, c); err != nil {
			t.Fatal(err)
		}
	}
	embed := &adapterEmbedder{forbidden: true}
	mem := memory.New(st, embed, logger)
	for folder, content := range map[string]string{"personal": "zebra PERSONAL_ONLY_CANARY", "group-legacy": "zebra RETIRED_GROUP_CANARY"} {
		if _, err = mem.AddLocalInThread(ctx, folder, 0, "fact", "", content, "test"); err != nil {
			t.Fatal(err)
		}
	}
	c, err := st.GetRegistered(ctx, "tg:-2002")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := st.RotateGroupEpoch(ctx, c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	current, err := st.CompleteGroupEpoch(ctx, migration.CurrentScope)
	if err != nil {
		t.Fatal(err)
	}
	return groupMemoryFixture{st: st, mem: mem, embedder: embed, groupFolder: current.Folder}
}

func TestGroupMemoryAdaptersUseOnlyLocalServices(t *testing.T) {
	f := newGroupMemoryFixture(t)
	ctx := context.Background()
	ops := &memoryOpsAdapter{st: f.st, mem: f.mem, ownerID: adapterOwnerID}
	compiler := &memoryCompilerAdapter{st: f.st, mem: f.mem, ownerID: adapterOwnerID}
	for _, folder := range []string{f.groupFolder, "forged-private-owner", "forged-group-owner", "unresolved-folder"} {
		t.Run(folder, func(t *testing.T) {
			id, err := ops.Add(ctx, folder, "fact", "local fact", "zebra GROUP_LOCAL_CANARY", "test")
			if err != nil {
				t.Fatal(err)
			}
			results, err := ops.SearchText(ctx, folder, "zebra", 5)
			if err != nil {
				t.Fatal(err)
			}
			if len(results) != 1 || results[0].ID != id || !strings.Contains(results[0].Content, "GROUP_LOCAL_CANARY") {
				t.Fatalf("wrong scoped local result: %#v", results)
			}
			compiled := compiler.CompileContext(ctx, folder, 7, "zebra")
			if !strings.Contains(compiled, "GROUP_LOCAL_CANARY") {
				t.Fatal("group compiler did not use local memory")
			}
			for _, foreign := range []string{"PERSONAL_ONLY_CANARY", "RETIRED_GROUP_CANARY"} {
				if strings.Contains(compiled, foreign) {
					t.Errorf("foreign data reached group compiler: %s", foreign)
				}
			}
			var model string
			if err = f.st.DB().QueryRowContext(ctx, `SELECT embed_model FROM memories WHERE id=?`, id).Scan(&model); err != nil {
				t.Fatal(err)
			}
			if model != "local-fts-only" {
				t.Fatal("group command used personal embedding provenance")
			}
		})
	}
	if f.embedder.calls != 0 {
		t.Fatal("group command called personal embedder")
	}
}

func TestMemoryAdapterOwnerIdentityComesFromConfiguredID(t *testing.T) {
	f := newGroupMemoryFixture(t)
	for _, tc := range []struct {
		name, folder string
		ownerID      int64
		local        bool
	}{
		{"configured-private-owner", "personal", adapterOwnerID, false},
		{"missing-owner-config", "personal", 0, true},
		{"negative-owner-config", "personal", -adapterOwnerID, true},
		{"other-owner-config", "personal", 9999, true},
		{"forged-private-owner-flag", "forged-private-owner", adapterOwnerID, true},
		{"forged-group-owner-flag", "forged-group-owner", adapterOwnerID, true},
		{"current-group-namespace", f.groupFolder, adapterOwnerID, true},
		{"unresolved-registration", "missing", adapterOwnerID, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := localMemoryScope(context.Background(), f.st, tc.folder, tc.ownerID); got != tc.local {
				t.Fatalf("local=%v want=%v", got, tc.local)
			}
		})
	}
}

func TestLocalOnlyMemoryFacadeWorksWithoutPersonalEmbedder(t *testing.T) {
	f := newGroupMemoryFixture(t)
	ctx := context.Background()
	mem := memory.New(f.st, nil, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ops := &memoryOpsAdapter{st: f.st, mem: mem, ownerID: adapterOwnerID, localOnly: true}
	compiler := &memoryCompilerAdapter{st: f.st, mem: mem, ownerID: adapterOwnerID, localOnly: true}
	if _, err := ops.Add(ctx, f.groupFolder, "fact", "", "zebra LOCAL_WITHOUT_KEY", "test"); err != nil {
		t.Fatal(err)
	}
	items, err := ops.SearchText(ctx, f.groupFolder, "zebra", 5)
	if err != nil || len(items) != 1 {
		t.Fatalf("local search unavailable: count=%d err=%v", len(items), err)
	}
	if !strings.Contains(compiler.CompileContext(ctx, f.groupFolder, 0, "zebra"), "LOCAL_WITHOUT_KEY") {
		t.Fatal("local compiler requires personal credentials")
	}
	if got := compiler.CompileContext(ctx, "personal", 0, "zebra"); got != "" {
		t.Fatal("nil-embedder fallback exposed private context")
	}
	if _, err = ops.Add(ctx, "personal", "fact", "", "must not write", "test"); err == nil {
		t.Fatal("private remote write falsely claimed success")
	}
	if _, err = ops.SearchText(ctx, "personal", "zebra", 5); err == nil {
		t.Fatal("private remote read falsely claimed availability")
	}
}

func TestPrivateMemoryAdapterRetainsPersonalEmbeddingBehavior(t *testing.T) {
	f := newGroupMemoryFixture(t)
	ctx := context.Background()
	f.embedder.forbidden = false
	ops := &memoryOpsAdapter{st: f.st, mem: f.mem, ownerID: adapterOwnerID}
	compiler := &memoryCompilerAdapter{st: f.st, mem: f.mem, ownerID: adapterOwnerID}
	if _, err := ops.Add(ctx, "personal", "fact", "", "zebra PRIVATE_REMOTE_CANARY", "test"); err != nil {
		t.Fatal(err)
	}
	if f.embedder.calls != 1 {
		t.Fatal("configured private write stopped using personal embedder")
	}
	before := f.embedder.calls
	if _, err := ops.SearchText(ctx, "personal", "zebra", 5); err != nil {
		t.Fatal(err)
	}
	if f.embedder.calls <= before {
		t.Fatal("configured private recall changed to group-only retrieval")
	}
	before = f.embedder.calls
	if got := compiler.CompileContext(ctx, "personal", 0, "zebra"); got == "" {
		t.Fatal("private memory context lost")
	}
	if f.embedder.calls <= before {
		t.Fatal("configured private compiler no longer uses configured embedding path")
	}
}
