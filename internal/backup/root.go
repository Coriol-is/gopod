package backup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// resolveLoose resolves symlinks in the longest existing prefix of p and
// appends the not yet existing remainder.
func resolveLoose(p string) (string, error) {
	p = filepath.Clean(p)
	rest := ""
	cur := p
	for {
		if r, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(r, rest), nil
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

func within(parent, child string) bool {
	if parent == child {
		return true
	}
	return strings.HasPrefix(child, strings.TrimRight(parent, string(filepath.Separator))+string(filepath.Separator))
}

// ValidateRoot checks that root is an absolute, host-private directory outside
// every forbidden tree (data dir, chats dir, repository, agent mounts) and
// creates it 0700 when missing. An existing root must be owned by the current
// user with no group or other access; it is never silently chmodded.
func ValidateRoot(root string, forbidden []string) error {
	if root == "" || !filepath.IsAbs(root) {
		return fmt.Errorf("%w: backup root must be an absolute path", ErrUnsafeRoot)
	}
	if fi, err := os.Lstat(root); err == nil && fi.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: backup root is a symlink", ErrUnsafeRoot)
	}
	rr, err := resolveLoose(root)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrUnsafeRoot, err)
	}
	for _, f := range forbidden {
		if f == "" {
			continue
		}
		rf, err := resolveLoose(f)
		if err != nil {
			return fmt.Errorf("%w: %v", ErrUnsafeRoot, err)
		}
		if within(rf, rr) || within(rr, rf) {
			return fmt.Errorf("%w: backup root overlaps %s", ErrUnsafeRoot, filepath.Base(rf))
		}
	}
	if err := os.MkdirAll(rr, 0o700); err != nil {
		return fmt.Errorf("backup: create root: %w", err)
	}
	var st unix.Stat_t
	if err := unix.Lstat(rr, &st); err != nil {
		return fmt.Errorf("backup: stat root: %w", err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Geteuid() || st.Mode&0o077 != 0 {
		return fmt.Errorf("%w: root must be a directory owned by this user with mode 0700", ErrUnsafeRoot)
	}
	return nil
}

func freeBytes(dir string) (uint64, error) {
	var fs unix.Statfs_t
	if err := unix.Statfs(dir, &fs); err != nil {
		return 0, err
	}
	return uint64(fs.Bavail) * uint64(fs.Bsize), nil
}

func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
