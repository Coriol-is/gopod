//go:build linux || darwin

package runner

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func protectedTestBase(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}
func protectedTestScope() store.ScopeRef {
	return store.ScopeRef{NamespaceID: "12345678-1234-4234-8234-123456789abc", NamespaceEpoch: 2, RegistrationRevision: 2, JID: "tg:-2", Folder: "ns-12345678123442348234123456789abc-e2"}
}
func TestProtectedNamespaceRootsAreSeparatedAndReadOnlyValidation(t *testing.T) {
	base := protectedTestBase(t)
	scope := protectedTestScope()
	if _, err := ValidateNamespacePaths(base, scope, NamespaceHostUID()); !errors.Is(err, ErrUnsafeNamespacePath) {
		t.Fatal("missing root validated")
	}
	if _, err := os.Stat(filepath.Join(base, "shared-namespaces")); !os.IsNotExist(err) {
		t.Fatal("validation wrote filesystem")
	}
	paths, err := PrepareNamespacePaths(base, scope, NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{paths.Workspace, paths.Memory, paths.Native, paths.IPC, paths.Policy} {
		info, e := os.Stat(path)
		if e != nil {
			t.Fatal(e)
		}
		if info.Mode().Perm() != 0o700 {
			t.Fatal("root permissions")
		}
		if path != paths.Workspace && strings.HasPrefix(path, paths.Workspace+string(os.PathSeparator)) {
			t.Fatal("protected source below writable workspace")
		}
	}
	if got, err := ValidateNamespacePaths(base, scope, NamespaceHostUID()); err != nil || got != paths {
		t.Fatalf("validate %v %v", got, err)
	}
	other := scope
	other.NamespaceID = "87654321-4321-4321-8321-cba987654321"
	other.Folder = "ns-87654321432143218321cba987654321-e2"
	other.JID = "tg:-3"
	p2, err := PrepareNamespacePaths(base, other, NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	if p2.Root == paths.Root || p2.Workspace == paths.Workspace {
		t.Fatal("cross namespace source reused")
	}
}
func TestProtectedNamespaceRejectsSymlinkMemoryAndParent(t *testing.T) {
	base := protectedTestBase(t)
	scope := protectedTestScope()
	paths, err := PrepareNamespacePaths(base, scope, NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	personal := protectedTestBase(t)
	sentinel := filepath.Join(personal, "private-marker")
	if err = os.WriteFile(sentinel, []byte("private-canary"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(paths.Memory, paths.Memory+"-saved"); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(personal, paths.Memory); err != nil {
		t.Fatal(err)
	}
	for _, create := range []bool{false, true} {
		if _, err = namespacePaths(base, scope, NamespaceHostUID(), create); !errors.Is(err, ErrUnsafeNamespacePath) {
			t.Fatal("symlink source accepted")
		}
	}
	contents, err := os.ReadFile(sentinel)
	if err != nil || string(contents) != "private-canary" {
		t.Fatal("outside state changed")
	}
	alias := filepath.Join(protectedTestBase(t), "data")
	if err = os.Symlink(base, alias); err != nil {
		t.Fatal(err)
	}
	if _, err = PrepareNamespacePaths(alias, scope, NamespaceHostUID()); !errors.Is(err, ErrUnsafeNamespacePath) {
		t.Fatal("symlink base accepted")
	}
}
func TestProtectedNamespaceRejectsUnsafeOwnershipModesAndIdentity(t *testing.T) {
	base := protectedTestBase(t)
	scope := protectedTestScope()
	if err := os.Chmod(base, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareNamespacePaths(base, scope, NamespaceHostUID()); !errors.Is(err, ErrUnsafeNamespacePath) {
		t.Fatal("writable parent accepted")
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*store.ScopeRef){
		func(s *store.ScopeRef) { s.NamespaceID = "../../owner" }, func(s *store.ScopeRef) { s.Folder = "owner" }, func(s *store.ScopeRef) { s.JID = "tg:1" }, func(s *store.ScopeRef) { s.NamespaceEpoch = 1 },
	} {
		changed := scope
		mutate(&changed)
		if _, err := PrepareNamespacePaths(base, changed, NamespaceHostUID()); !errors.Is(err, ErrUnsafeNamespacePath) {
			t.Fatal("invalid scope accepted")
		}
	}
	paths, err := PrepareNamespacePaths(base, scope, NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(paths.Root, 0o775); err != nil {
		t.Fatal(err)
	}
	if _, err = ValidateNamespacePaths(base, scope, NamespaceHostUID()); !errors.Is(err, ErrUnsafeNamespacePath) {
		t.Fatal("writable namespace parent accepted")
	}
	if _, err = PrepareNamespacePaths(base, scope, NamespaceHostUID()+1); !errors.Is(err, ErrUnsafeNamespacePath) {
		t.Fatal("foreign owner accepted")
	}
}
