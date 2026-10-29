package telegram

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/go-telegram/bot/models"
)

func TestUploadSymlinkCannotWriteOutsideWorkspace(t *testing.T) {
	fixture := newInboundFixture(t, nil)
	outside := t.TempDir()
	if err := os.MkdirAll("owner", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join("owner", "uploads")); err != nil {
		t.Fatal(err)
	}
	m := threadedMsg(0, "")
	m.Document = &models.Document{FileID: "doc", FileName: "outside.txt"}
	fixture.handle(m)
	fixture.none(t)
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside host directory modified")
	}
	if err = os.Remove(filepath.Join("owner", "uploads")); err != nil {
		t.Fatal(err)
	}
	m.ID++
	fixture.handle(m)
	fixture.next(t)
	if entries, err = os.ReadDir(filepath.Join("owner", "uploads")); err != nil || len(entries) != 1 {
		t.Fatal("legitimate upload blocked")
	}
}

type uploadGroupRuntime struct{}

func (uploadGroupRuntime) Authorize(context.Context, store.RegisteredChat) error { return nil }
func (uploadGroupRuntime) Start(context.Context, store.RegisteredChat, runner.AgentProvider, int, string) (string, runner.AgentProvider, func(), error) {
	panic("upload must not start inference")
}

func TestGroupUploadUsesProtectedWorkspaceWithoutLegacyAlias(t *testing.T) {
	f := newInboundFixture(t, nil)
	ctx := t.Context()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err = f.b.store.RegisterChat(ctx, store.RegisteredChat{JID: "tg:-200", Folder: "old-group", AddedAt: 1}); err != nil {
		t.Fatal(err)
	}
	// A group registered before D048 keeps answering every message.
	if err = f.b.store.SetGroupTrigger(ctx, "tg:-200", store.TriggerAll, 1); err != nil {
		t.Fatal(err)
	}
	group, _ := f.b.store.GetRegistered(ctx, "tg:-200")
	migration, err := f.b.store.RotateGroupEpoch(ctx, group.JID, group.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := runner.PrepareNamespacePaths(base, migration.CurrentScope, runner.NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.b.store.CompleteGroupEpoch(ctx, migration.CurrentScope); err != nil {
		t.Fatal(err)
	}
	agent, err := runner.New(&runner.Docker{}, runner.NewClaudeProvider(""), runner.Paths{RepoRoot: base, DataDir: base, ChatsDir: filepath.Join(base, "legacy"), ContainerSkillsDir: filepath.Join(base, "skills"), EmptyFile: filepath.Join(base, "empty")}, runner.SpawnDefaults{}, nil, "test", nil)
	if err != nil {
		t.Fatal(err)
	}
	agent.SetAdmissionPolicy(streamChat, f.b.store)
	agent.SetGroupRuntime(uploadGroupRuntime{})
	f.b.runner = agent
	m := threadedMsg(0, "")
	m.Chat.ID = -200
	m.Chat.Type = models.ChatTypeSupergroup
	m.Document = &models.Document{FileID: "groupdoc", FileName: "group.txt"}
	f.handle(m)
	item := f.next(t)
	if _, err = os.Stat(filepath.Join(paths.Workspace, "uploads", filepath.Base(item.FilePath))); err != nil {
		t.Fatal("protected upload absent")
	}
	if _, err = os.Stat(filepath.Join(base, "legacy", migration.CurrentScope.Folder)); !os.IsNotExist(err) {
		t.Fatal("legacy current-alias directory created")
	}
	if _, err = os.Stat(filepath.Join(base, "legacy", "old-group")); !os.IsNotExist(err) {
		t.Fatal("retired group workspace touched")
	}
}
