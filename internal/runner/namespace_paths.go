//go:build linux || darwin

package runner

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/store"
	"golang.org/x/sys/unix"
)

var ErrUnsafeNamespacePath = errors.New("runner: protected namespace filesystem unavailable")

// NamespacePaths separates every independently mounted surface. In particular,
// neither native state, policy nor memory is below the agent-writable workspace.
// Only the trusted CLI may mount Native; untrusted tools receive Workspace only.
// Parents are controller-owned and must never be mounted into an agent runtime.
type NamespacePaths struct {
	Root      string
	Workspace string
	Memory    string
	IPC       string
	Native    string
	Policy    string
}

// PrepareNamespacePaths creates an empty clean group epoch. It never imports
// legacy files, credentials, memory or tasks. Its host root must be an absolute,
// canonical path; symlinks in any component are rejected. All traversal and
// creation uses held parent descriptors with O_NOFOLLOW, not check-then-open.
func PrepareNamespacePaths(dataDir string, scope store.ScopeRef, ownerUID int) (NamespacePaths, error) {
	return namespacePaths(dataDir, scope, ownerUID, true)
}

// ValidateNamespacePaths is a read-only admission check of the same structure.
// Runtime mount assembly must keep these parents outside all writable mounts.
func ValidateNamespacePaths(dataDir string, scope store.ScopeRef, ownerUID int) (NamespacePaths, error) {
	return namespacePaths(dataDir, scope, ownerUID, false)
}

func namespacePaths(dataDir string, scope store.ScopeRef, ownerUID int, create bool) (NamespacePaths, error) {
	if !store.ValidScopeIdentity(scope) || scope.NamespaceEpoch <= 1 || ownerUID < 0 || !filepath.IsAbs(dataDir) || filepath.Clean(dataDir) != dataDir {
		return NamespacePaths{}, ErrUnsafeNamespacePath
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(scope.JID, "tg:"), 10, 64)
	if err != nil || id >= 0 || scope.JID != "tg:"+strconv.FormatInt(id, 10) {
		return NamespacePaths{}, ErrUnsafeNamespacePath
	}
	key := "ns-" + strings.ReplaceAll(scope.NamespaceID, "-", "") + "-e" + strconv.FormatInt(scope.NamespaceEpoch, 10)
	if scope.Folder != key {
		return NamespacePaths{}, ErrUnsafeNamespacePath
	}
	fd, err := openProtectedBase(dataDir, ownerUID)
	if err != nil {
		return NamespacePaths{}, ErrUnsafeNamespacePath
	}
	defer func() { unix.Close(fd) }()
	parts := []string{"shared-namespaces", scope.NamespaceID, "e" + strconv.FormatInt(scope.NamespaceEpoch, 10)}
	for _, part := range parts {
		next, e := openNamespaceChild(fd, part, ownerUID, create)
		if e != nil {
			return NamespacePaths{}, ErrUnsafeNamespacePath
		}
		unix.Close(fd)
		fd = next
	}
	root := filepath.Join(append([]string{dataDir}, parts...)...)
	out := NamespacePaths{Root: root, Workspace: filepath.Join(root, "workspace"), Memory: filepath.Join(root, "memory"), IPC: filepath.Join(root, "ipc"), Native: filepath.Join(root, "native"), Policy: filepath.Join(root, "policy")}
	for _, name := range []string{"workspace", "memory", "ipc", "native", "policy"} {
		child, e := openNamespaceChild(fd, name, ownerUID, create)
		if e != nil {
			return NamespacePaths{}, ErrUnsafeNamespacePath
		}
		unix.Close(child)
	}
	for _, pair := range [][2]string{{"native", "claude"}, {"native", "codex"}, {"ipc", "messages"}, {"ipc", "tasks"}, {"ipc", "results"}, {"ipc", "input"}, {"ipc", ".processed"}, {"ipc", ".failed"}} {
		parent, e := openNamespaceChild(fd, pair[0], ownerUID, false)
		if e != nil {
			return NamespacePaths{}, ErrUnsafeNamespacePath
		}
		child, e := openNamespaceChild(parent, pair[1], ownerUID, create)
		unix.Close(parent)
		if e != nil {
			return NamespacePaths{}, ErrUnsafeNamespacePath
		}
		unix.Close(child)
	}
	return out, nil
}

func openProtectedBase(path string, ownerUID int) (int, error) {
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for i, name := range parts {
		if name == "" {
			continue
		}
		next, e := unix.Openat(fd, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		unix.Close(fd)
		if e != nil {
			return -1, e
		}
		fd = next
		var stat unix.Stat_t
		if e = unix.Fstat(fd, &stat); e != nil {
			unix.Close(fd)
			return -1, e
		}
		if stat.Uid != 0 && int(stat.Uid) != ownerUID {
			unix.Close(fd)
			return -1, ErrUnsafeNamespacePath
		}
		// A root-owned sticky system temp ancestor is allowed for isolated tests;
		// the selected DataDir itself and all managed parents must be non-writable
		// by other users. No path is resolved through a symlink.
		if stat.Mode&0o022 != 0 && (i == len(parts)-1 || stat.Uid != 0 || stat.Mode&unix.S_ISVTX == 0) {
			unix.Close(fd)
			return -1, ErrUnsafeNamespacePath
		}
	}
	return fd, nil
}

func openNamespaceChild(parent int, name string, ownerUID int, create bool) (int, error) {
	if name == "" || strings.ContainsAny(name, "/\\") || name == "." || name == ".." {
		return -1, ErrUnsafeNamespacePath
	}
	if create {
		if err := unix.Mkdirat(parent, name, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	fd, err := unix.Openat(parent, name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return -1, err
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		unix.Close(fd)
		return -1, err
	}
	if int(stat.Uid) != ownerUID || stat.Mode&0o022 != 0 {
		unix.Close(fd)
		return -1, fmt.Errorf("%w: invalid ownership or mode", ErrUnsafeNamespacePath)
	}
	return fd, nil
}

// NamespaceHostUID is the controller identity expected to own protected roots.
// Group tools can share its numeric uid only inside containers with no parent
// mounts; host filesystem ownership alone is not a container isolation claim.
func NamespaceHostUID() int { return os.Geteuid() }
