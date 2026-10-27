package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

type workspaceOnlyGroup struct{}

func (workspaceOnlyGroup) Authorize(context.Context, store.RegisteredChat) error { return nil }
func (workspaceOnlyGroup) Start(context.Context, store.RegisteredChat, AgentProvider, int, string) (string, AgentProvider, func(), error) {
	panic("workspace resolution must not start runtime")
}

func TestChatWorkspaceUsesQualifiedProtectedSourceAndNoLegacyFallback(t *testing.T) {
	base := protectedTestBase(t)
	scope := protectedTestScope()
	paths, err := PrepareNamespacePaths(base, scope, NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	group := store.RegisteredChat{JID: scope.JID, Folder: scope.Folder, NamespaceID: scope.NamespaceID, NamespaceEpoch: scope.NamespaceEpoch, RegistrationRevision: scope.RegistrationRevision, GroupEpochReady: true}
	owner := store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true}
	r := &Runner{paths: Paths{DataDir: base, ChatsDir: filepath.Join(base, "legacy")}, groups: workspaceOnlyGroup{}}
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{owner, group}})
	path, err := r.ChatWorkspace(t.Context(), group.Folder)
	if err != nil || path != paths.Workspace {
		t.Fatalf("protected source %s %v", path, err)
	}
	if got, err := r.ChatWorkspace(t.Context(), "owner"); err != nil || got != filepath.Join(base, "legacy", "owner") {
		t.Fatal("personal path changed")
	}
	for _, folder := range []string{"retired-group", "missing"} {
		if _, err = r.ChatWorkspace(t.Context(), folder); !errors.Is(err, ErrProfileUnavailable) {
			t.Fatal("unknown/retired legacy fallback")
		}
	}
	group.GroupEpochReady = false
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{owner, group}})
	if _, err = r.ChatWorkspace(t.Context(), group.Folder); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatal("incomplete epoch fallback")
	}
	group.GroupEpochReady = true
	r.SetAdmissionPolicy(1, profileRegistrations{rows: []store.RegisteredChat{owner, group}})
	if err = os.Rename(paths.Memory, paths.Memory+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(protectedTestBase(t), paths.Memory); err != nil {
		t.Fatal(err)
	}
	if _, err = r.ChatWorkspace(t.Context(), group.Folder); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatal("unsafe protected source fallback")
	}
}

type workspaceRetiringRows struct {
	calls int
	group store.RegisteredChat
}

func (r *workspaceRetiringRows) ListRegistered(context.Context) ([]store.RegisteredChat, error) {
	r.calls++
	if r.calls == 1 {
		return []store.RegisteredChat{r.group}, nil
	}
	return nil, nil
}
func TestChatWorkspaceRetirementRaceNeverFallsBack(t *testing.T) {
	scope := protectedTestScope()
	group := store.RegisteredChat{JID: scope.JID, Folder: scope.Folder, NamespaceID: scope.NamespaceID, NamespaceEpoch: scope.NamespaceEpoch, RegistrationRevision: scope.RegistrationRevision, GroupEpochReady: true}
	r := &Runner{paths: Paths{ChatsDir: "legacy"}, groups: workspaceOnlyGroup{}}
	r.SetAdmissionPolicy(1, &workspaceRetiringRows{group: group})
	if _, err := r.ChatWorkspace(t.Context(), group.Folder); !errors.Is(err, ErrProfileUnavailable) {
		t.Fatal("retirement raced into legacy path")
	}
}
