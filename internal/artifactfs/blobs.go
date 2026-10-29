//go:build linux || darwin

package artifactfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

var (
	prepNamePattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)
	blobNamePattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}\.blob$`)
)

const copyChunk = 64 << 10

// Blobs is the host-private snapshot store: <root>/<preparation-uuid>/<artifact-uuid>.blob.
// The root and each preparation directory are mode 0700 and owned by the host
// user, files are 0600, and every access is a no-follow walk from the held root
// descriptor. Nothing here is writable by the agent.
type Blobs struct {
	root string
	fd   int
	// Fault is an optional fault-injection hook for tests; see FaultFunc.
	Fault FaultFunc
	// freeBytes reports free space for the root; replaceable in tests.
	freeBytes func() (uint64, error)
}

// OpenBlobs creates (if needed) and opens the blob root, refusing a root that is
// not a directory owned by the current user with no group/other access.
func OpenBlobs(root string) (*Blobs, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("artifactfs: create blob root: %w", err)
	}
	fd, err := unix.Open(root, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno(err)
	}
	var st unix.Stat_t
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, mapErrno(err)
	}
	if st.Mode&unix.S_IFMT != unix.S_IFDIR || int(st.Uid) != os.Geteuid() || st.Mode&0o077 != 0 {
		unix.Close(fd)
		return nil, ErrUnsafeRoot
	}
	b := &Blobs{root: root, fd: fd}
	b.freeBytes = func() (uint64, error) {
		var fs unix.Statfs_t
		if e := unix.Fstatfs(b.fd, &fs); e != nil {
			return 0, mapErrno(e)
		}
		return uint64(fs.Bavail) * uint64(fs.Bsize), nil
	}
	return b, nil
}

// Close releases the root descriptor.
func (b *Blobs) Close() error {
	if b == nil || b.fd < 0 {
		return nil
	}
	err := unix.Close(b.fd)
	b.fd = -1
	return err
}

// FreeBytes reports the free space available to the host user on the blob volume.
func (b *Blobs) FreeBytes() (uint64, error) { return b.freeBytes() }

// SetFreeBytesForTest replaces the free-space probe.
func (b *Blobs) SetFreeBytesForTest(fn func() (uint64, error)) { b.freeBytes = fn }

func (b *Blobs) fault(point string) error {
	if b.Fault == nil {
		return nil
	}
	return b.Fault(point)
}

// splitKey validates a relative_blob_key from the inventory.
func splitKey(key string) (prep, name string, err error) {
	prep, name, ok := strings.Cut(key, "/")
	if !ok || !prepNamePattern.MatchString(prep) || !blobNamePattern.MatchString(name) {
		return "", "", ErrUnsafePath
	}
	return prep, name, nil
}

func (b *Blobs) openPrep(prep string) (int, error) {
	if !prepNamePattern.MatchString(prep) {
		return -1, ErrUnsafePath
	}
	fd, err := unix.Openat(b.fd, prep, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return -1, ErrPreparationMissing
	}
	if err != nil {
		return -1, mapErrno(err)
	}
	return fd, nil
}

// NewPreparation creates the private directory of one preparation and makes it
// durable. It fails if the directory already exists.
func (b *Blobs) NewPreparation(prep string) error {
	if !prepNamePattern.MatchString(prep) {
		return ErrUnsafePath
	}
	if err := unix.Mkdirat(b.fd, prep, 0o700); err != nil {
		return mapErrno(err)
	}
	if err := b.fault("after_prep_mkdir"); err != nil {
		return err
	}
	if err := unix.Fsync(b.fd); err != nil {
		return mapErrno(err)
	}
	return nil
}

// Snapshot is the verified result of one bounded copy.
type Snapshot struct {
	SHA256 string
	Size   int64
}

// Snapshot copies the open source into <prep>/<name> as a private snapshot,
// hashing while it copies. It reads at most maxBytes+1, fails if the source is
// not exactly its open-time size, fsyncs the file and the directory, then
// re-reads the source once more and compares the hash, so a change during
// preparation fails the request even where timestamps are too coarse to show it.
func (b *Blobs) Snapshot(prep, name string, src *SourceFile, maxBytes int64) (snap Snapshot, err error) {
	if !blobNamePattern.MatchString(name) {
		return Snapshot{}, ErrUnsafePath
	}
	dir, err := b.openPrep(prep)
	if err != nil {
		return Snapshot{}, err
	}
	defer unix.Close(dir)
	part := name + ".part"
	fd, err := unix.Openat(dir, part, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
	if err != nil {
		return Snapshot{}, mapErrno(err)
	}
	out := os.NewFile(uintptr(fd), part)
	crashed := false
	defer func() {
		out.Close()
		if err != nil && !crashed {
			_ = unix.Unlinkat(dir, part, 0)
		}
	}()
	fail := func(e error) (Snapshot, error) {
		crashed = errors.Is(e, ErrSimulatedCrash)
		return Snapshot{}, e
	}

	h := sha256.New()
	var n int64
	buf := make([]byte, copyChunk)
	for first := true; ; first = false {
		k, rerr := src.File.Read(buf)
		if k > 0 {
			n += int64(k)
			if n > maxBytes || n > src.st.Size {
				return fail(sourceGrew(n, maxBytes))
			}
			h.Write(buf[:k])
			if _, werr := out.Write(buf[:k]); werr != nil {
				return fail(mapErrno(werr))
			}
			if first {
				if ferr := b.fault("copy_mid"); ferr != nil {
					return fail(ferr)
				}
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return fail(fmt.Errorf("artifactfs: read source: %w", rerr))
		}
	}
	if ferr := b.fault("copy_end"); ferr != nil {
		return fail(ferr)
	}
	if n != src.st.Size {
		return fail(ErrSourceChanged)
	}
	if serr := src.Stable(); serr != nil {
		return fail(serr)
	}
	sum := hex.EncodeToString(h.Sum(nil))
	if serr := rehashContent(src, sum); serr != nil {
		return fail(serr)
	}
	if serr := src.Stable(); serr != nil {
		return fail(serr)
	}
	if serr := out.Sync(); serr != nil {
		return fail(mapErrno(serr))
	}
	if ferr := b.fault("after_file_fsync"); ferr != nil {
		return fail(ferr)
	}
	if rerr := unix.Renameat(dir, part, dir, name); rerr != nil {
		return fail(mapErrno(rerr))
	}
	if ferr := b.fault("after_rename"); ferr != nil {
		// The final name exists but is not durable or in the inventory; a
		// failed (non-crash) attempt removes it, a crash leaves it for reconcile.
		if !errors.Is(ferr, ErrSimulatedCrash) {
			_ = unix.Unlinkat(dir, name, 0)
		}
		return fail(ferr)
	}
	if serr := unix.Fsync(dir); serr != nil {
		_ = unix.Unlinkat(dir, name, 0)
		return fail(mapErrno(serr))
	}
	if ferr := b.fault("after_dir_fsync"); ferr != nil {
		return fail(ferr)
	}
	return Snapshot{SHA256: sum, Size: n}, nil
}

func sourceGrew(n, maxBytes int64) error {
	if n > maxBytes {
		return ErrTooLarge
	}
	return ErrSourceChanged
}

// rehashContent reads the source again from the start and compares the digest.
// It is the check that still works where timestamps are too coarse to show a
// change, and it does not depend on metadata at all.
func rehashContent(src *SourceFile, want string) error {
	if _, err := src.File.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("artifactfs: rewind source: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(src.File, src.st.Size+1))
	if err != nil {
		return fmt.Errorf("artifactfs: reread source: %w", err)
	}
	if n != src.st.Size || hex.EncodeToString(h.Sum(nil)) != want {
		return ErrSourceChanged
	}
	return nil
}

// openBlob walks key and returns an open, verified regular file (content is verified by the caller's hash).
func (b *Blobs) openBlob(key string) (*os.File, unix.Stat_t, error) {
	var st unix.Stat_t
	prep, name, err := splitKey(key)
	if err != nil {
		return nil, st, err
	}
	dir, err := b.openPrep(prep)
	if err != nil {
		return nil, st, err
	}
	defer unix.Close(dir)
	fd, err := unix.Openat(dir, name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_NOCTTY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, st, mapErrno(err)
	}
	if err = unix.Fstat(fd, &st); err != nil {
		unix.Close(fd)
		return nil, st, mapErrno(err)
	}
	// No link-count check: the store is private and the hash proves content.
	if st.Mode&unix.S_IFMT != unix.S_IFREG {
		unix.Close(fd)
		return nil, st, ErrCorrupt
	}
	return os.NewFile(uintptr(fd), name), st, nil
}

// StatBlob reports the size of a stored blob, failing if it is missing or not a
// regular file. It does not read the content.
func (b *Blobs) StatBlob(key string) (int64, error) {
	f, st, err := b.openBlob(key)
	if err != nil {
		return 0, err
	}
	f.Close()
	return st.Size, nil
}

// ReadVerified opens the blob without following links, checks its type and
// size against the inventory, reads exactly size bytes into a bounded
// buffer and compares SHA-256. The caller uploads these bytes and never reopens
// a pathname. A missing blob is ErrNotFound; any mismatch is ErrCorrupt.
func (b *Blobs) ReadVerified(key string, size int64, sha string, maxBytes int64) ([]byte, error) {
	if size < 0 || size > maxBytes || len(sha) != 64 {
		return nil, ErrCorrupt
	}
	f, st, err := b.openBlob(key)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if st.Size != size {
		return nil, ErrCorrupt
	}
	data, err := io.ReadAll(io.LimitReader(f, size+1))
	if err != nil {
		return nil, fmt.Errorf("artifactfs: read blob: %w", err)
	}
	sum := sha256.Sum256(data)
	if int64(len(data)) != size || !strings.EqualFold(hex.EncodeToString(sum[:]), sha) {
		return nil, ErrCorrupt
	}
	return data, nil
}

// RemoveBlob unlinks one blob and its partial file; it is idempotent. The
// preparation directory is removed once empty.
func (b *Blobs) RemoveBlob(key string) error {
	prep, name, err := splitKey(key)
	if err != nil {
		return err
	}
	dir, err := b.openPrep(prep)
	if errors.Is(err, ErrPreparationMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, n := range []string{name, name + ".part"} {
		if e := unix.Unlinkat(dir, n, 0); e != nil && !errors.Is(e, unix.ENOENT) {
			unix.Close(dir)
			return mapErrno(e)
		}
	}
	if err = unix.Fsync(dir); err != nil {
		unix.Close(dir)
		return mapErrno(err)
	}
	unix.Close(dir)
	// Best effort: ENOTEMPTY means other artifacts of the preparation remain.
	if e := unix.Unlinkat(b.fd, prep, unix.AT_REMOVEDIR); e == nil {
		return mapSync(b.fd)
	}
	return nil
}

func mapSync(fd int) error {
	if err := unix.Fsync(fd); err != nil {
		return mapErrno(err)
	}
	return nil
}

// RemovePreparation removes every file of a preparation directory and the
// directory itself; it is idempotent and never follows a link.
func (b *Blobs) RemovePreparation(prep string) error {
	dir, err := b.openPrep(prep)
	if errors.Is(err, ErrPreparationMissing) {
		return nil
	}
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(dir), prep)
	names, rerr := f.Readdirnames(-1)
	if rerr != nil {
		f.Close()
		return fmt.Errorf("artifactfs: list preparation: %w", rerr)
	}
	for _, n := range names {
		if e := unix.Unlinkat(dir, n, 0); e != nil && !errors.Is(e, unix.ENOENT) {
			f.Close()
			return mapErrno(e)
		}
	}
	f.Close()
	if e := unix.Unlinkat(b.fd, prep, unix.AT_REMOVEDIR); e != nil && !errors.Is(e, unix.ENOENT) {
		return mapErrno(e)
	}
	return mapSync(b.fd)
}

// PreparationDir is a host-owned preparation directory found on disk.
type PreparationDir struct {
	Name    string
	ModTime time.Time
}

// ListPreparations lists well-formed preparation directories directly below the
// root. Entries with any other name or type are never reported and never touched.
func (b *Blobs) ListPreparations() ([]PreparationDir, error) {
	// A fresh open file description: a dup would share (and exhaust) the offset.
	dup, err := unix.Openat(b.fd, ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, mapErrno(err)
	}
	f := os.NewFile(uintptr(dup), b.root)
	defer f.Close()
	entries, err := f.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("artifactfs: list blob root: %w", err)
	}
	var out []PreparationDir
	for _, e := range entries {
		if !e.IsDir() || !prepNamePattern.MatchString(e.Name()) {
			continue
		}
		info, ierr := e.Info()
		if ierr != nil {
			continue
		}
		out = append(out, PreparationDir{Name: e.Name(), ModTime: info.ModTime()})
	}
	return out, nil
}

// PreparationBlobs lists the file names directly inside a preparation.
func (b *Blobs) PreparationBlobs(prep string) ([]string, error) {
	dir, err := b.openPrep(prep)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(dir), prep)
	defer f.Close()
	return f.Readdirnames(-1)
}

// RootForTest returns the blob root path so tests can tamper with files.
func (b *Blobs) RootForTest() string { return b.root }
