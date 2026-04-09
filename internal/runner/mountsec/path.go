package mountsec

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// chatFolderPattern is the validation pattern for chat folder names
// referenced in ExtraMount.AllowedChats. Mirrors ISOLATION.md §4.3 step 7.
//
// Why this shape: chat folder names also become directory names on disk
// and parts of file paths in the IPC layer; restricting them to a small
// alphabet eliminates path traversal, shell metacharacter, and case
// confusion problems before they have a chance to start.
const chatFolderPattern = `^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`

// validHostPath enforces the host_path rules from ISOLATION.md §4.3:
//   - non-empty
//   - absolute
//   - clean (no .., no // collapses)
//   - exists
//   - is a directory
//   - resolved via filepath.EvalSymlinks to an absolute path
//   - resolved path does not match a blocked pattern
//
// Returns the resolved (post-symlink) path on success.
func validHostPath(p string) (string, error) {
	if p == "" {
		return "", errors.New("host_path is empty")
	}
	if !filepath.IsAbs(p) {
		return "", fmt.Errorf("host_path %q is not absolute", p)
	}
	clean := filepath.Clean(p)
	if clean != p {
		return "", fmt.Errorf("host_path %q is not in clean form (would be %q)", p, clean)
	}
	if containsParentTraversal(p) {
		return "", fmt.Errorf("host_path %q contains parent-directory traversal", p)
	}

	info, err := os.Stat(p)
	if err != nil {
		return "", fmt.Errorf("host_path %q: %w", p, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("host_path %q is not a directory", p)
	}

	resolved, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", fmt.Errorf("host_path %q: resolve symlinks: %w", p, err)
	}
	if !filepath.IsAbs(resolved) {
		return "", fmt.Errorf("host_path %q resolved to non-absolute %q", p, resolved)
	}
	if IsBlocked(resolved) {
		return "", fmt.Errorf("host_path %q resolves to blocked path %q", p, resolved)
	}

	return resolved, nil
}

// validContainerPath enforces the container_path rules from
// ISOLATION.md §4.3 step 6.
func validContainerPath(p string) error {
	if p == "" {
		return errors.New("container_path is empty")
	}
	if !strings.HasPrefix(p, "/workspace/extra/") {
		return fmt.Errorf("container_path %q must start with /workspace/extra/", p)
	}
	clean := filepath.Clean(p)
	if clean != p {
		return fmt.Errorf("container_path %q is not in clean form (would be %q)", p, clean)
	}
	if containsParentTraversal(p) {
		return fmt.Errorf("container_path %q contains parent-directory traversal", p)
	}
	// /workspace/extra alone (no trailing dir) doesn't carve out anything.
	if p == "/workspace/extra" || p == "/workspace/extra/" {
		return fmt.Errorf("container_path %q is missing a leaf directory under /workspace/extra/", p)
	}
	return nil
}

// containsParentTraversal reports whether p has a `..` segment anywhere.
// Independent of filepath.Clean to catch the (already-cleaned-but-still-bad)
// case where the original input was something like `/a/../b`. Cleanliness
// is checked separately.
func containsParentTraversal(p string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(p), "/") {
		if seg == ".." {
			return true
		}
	}
	return false
}
