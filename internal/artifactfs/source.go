//go:build linux || darwin

package artifactfs

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	maxRelPath  = 1024
	maxRelDepth = 16
)

// SourceRoot is the authorized directory (the run's ordinary output root). The
// root path itself is host configuration and is opened normally; everything
// below it is walked one component at a time with O_NOFOLLOW from a held
// descriptor, so no component can be a link and nothing is checked-then-reopened.
type SourceRoot struct{ fd int }

// OpenSourceRoot opens the host-chosen authorized directory.
func OpenSourceRoot(dir string) (*SourceRoot, error) {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno(err)
	}
	return &SourceRoot{fd: fd}, nil
}

// Close releases the root descriptor.
func (r *SourceRoot) Close() error {
	if r == nil || r.fd < 0 {
		return nil
	}
	err := unix.Close(r.fd)
	r.fd = -1
	return err
}

// SourceFile is a verified, open regular file with the metadata seen at open.
type SourceFile struct {
	File *os.File
	st   unix.Stat_t
}

// Size is the size seen at open.
func (f *SourceFile) Size() int64 { return f.st.Size }

// Close closes the descriptor.
func (f *SourceFile) Close() error { return f.File.Close() }

// Stable re-reads the descriptor's metadata and fails if identity, size,
// modification time, change time or link count differ from the open-time view.
func (f *SourceFile) Stable() error {
	var now unix.Stat_t
	if err := unix.Fstat(int(f.File.Fd()), &now); err != nil {
		return mapErrno(err)
	}
	if now.Dev != f.st.Dev || now.Ino != f.st.Ino || now.Size != f.st.Size || now.Nlink != 1 ||
		mtimeNs(&now) != mtimeNs(&f.st) || ctimeNs(&now) != ctimeNs(&f.st) {
		return ErrSourceChanged
	}
	return nil
}

// cleanRel validates a root-relative path: no absolute path, no empty, "." or
// ".." component, no NUL, bounded length and depth, and already in clean form.
func cleanRel(rel string) ([]string, error) {
	if rel == "" || len(rel) > maxRelPath || strings.ContainsRune(rel, 0) || strings.HasPrefix(rel, "/") || strings.HasSuffix(rel, "/") {
		return nil, ErrUnsafePath
	}
	parts := strings.Split(rel, "/")
	if len(parts) > maxRelDepth {
		return nil, ErrUnsafePath
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			return nil, ErrUnsafePath
		}
	}
	return parts, nil
}

// Open walks rel below the root without following any link and returns the
// regular file only if its link count is one and its size is within maxBytes.
// The returned descriptor is the only handle later steps use.
func (r *SourceRoot) Open(rel string, maxBytes int64) (*SourceFile, error) {
	parts, err := cleanRel(rel)
	if err != nil {
		return nil, err
	}
	dir, owned, err := walkDirs(r.fd, parts[:len(parts)-1])
	if err != nil {
		return nil, err
	}
	if owned {
		defer unix.Close(dir)
	}
	name := parts[len(parts)-1]
	var pre unix.Stat_t
	if err = unix.Fstatat(dir, name, &pre, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return nil, mapErrno(err)
	}
	if err = checkRegular(&pre, maxBytes); err != nil {
		return nil, err
	}
	// O_NONBLOCK so that a FIFO swapped in after the check cannot block us.
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno(err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, mapErrno(err)
	}
	if err = checkRegular(&st, maxBytes); err != nil {
		unix.Close(fd)
		return nil, err
	}
	if st.Dev != pre.Dev || st.Ino != pre.Ino {
		unix.Close(fd)
		return nil, ErrSourceChanged
	}
	return &SourceFile{File: os.NewFile(uintptr(fd), name), st: st}, nil
}

func checkRegular(st *unix.Stat_t, maxBytes int64) error {
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFREG:
	case unix.S_IFLNK:
		return ErrUnsafePath
	default:
		return ErrNotRegular
	}
	if st.Nlink != 1 {
		return ErrHardlinked
	}
	if st.Size < 0 || st.Size > maxBytes {
		return ErrTooLarge
	}
	return nil
}

// walkDirs opens each directory component with O_NOFOLLOW. owned reports
// whether the returned descriptor must be closed by the caller.
func walkDirs(start int, parts []string) (fd int, owned bool, err error) {
	cur := start
	for _, p := range parts {
		next, e := unix.Openat(cur, p, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if cur != start {
			unix.Close(cur)
		}
		if e != nil {
			return -1, false, mapErrno(e)
		}
		cur = next
	}
	return cur, cur != start, nil
}

// mapErrno turns a syscall error into the package's typed errors. Anything a
// no-follow open reports for a link (ELOOP, ENOTDIR on a link to a directory)
// is an unsafe path, not a missing file.
func mapErrno(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return ErrNotFound
	case errors.Is(err, unix.ELOOP), errors.Is(err, unix.ENOTDIR), errors.Is(err, unix.EMLINK):
		return ErrUnsafePath
	case errors.Is(err, unix.ENOSPC), errors.Is(err, unix.EDQUOT):
		return ErrStorageFull
	}
	return fmt.Errorf("artifactfs: %w", err)
}
