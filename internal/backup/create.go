package backup

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"github.com/Coriol-is/gopod/internal/artifactfs"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

// DefaultMaxArtifactBytes bounds one artifact copied into a set.
const DefaultMaxArtifactBytes int64 = 256 << 20

// Options configure Create. Nothing is read from the environment.
type Options struct {
	Root         string // host-private backup root, outside every source tree
	Mode         string // ModeDatabaseOnly or ModeMatchedStopped
	StorePath    string // SQLite database file
	ArtifactsDir string // artifact blob root (matched mode); may not exist yet
	ConfigFiles  []string
	Forbidden    []string // extra trees the root must not overlap (chats dir, repo)
	Version      string

	// Settlement is required for ModeMatchedStopped.
	Settlement *maintenance.Settlement

	// MaxBytes caps the total size of all sets under Root including the new
	// one; 0 means no cap beyond free space.
	MaxBytes         int64
	MaxArtifactBytes int64
	// StagingMaxAge is how old an unlocked staging entry must be before Create
	// removes it as the leftover of a crashed capture (default 30 minutes).
	StagingMaxAge time.Duration

	Now       func() time.Time
	Fault     func(point string) error
	FreeBytes func(dir string) (uint64, error)
	Log       *slog.Logger
}

// Result describes a published set.
type Result struct {
	SetID    string
	Path     string
	Manifest Manifest
}

// Fault point names shared by the writers.
const (
	faultFileWrite = "file.write"
	faultFileSync  = "file.sync"
)

// Fault points, in the order they are reached. Tests inject a failure at each.
var FaultPoints = []string{
	"stage.mkdir", "snapshot", "snapshot.create", "snapshot.copy_done", "snapshot.finalize", "snapshot.sync", faultFileWrite, faultFileSync, "manifest.write", "manifest.sync",
	"marker.write", "marker.sync", "stage.dirsync", "publish.rename", "root.dirsync",
}

func (o *Options) fault(point string) error {
	if o.Fault == nil {
		return nil
	}
	return o.Fault(point)
}

func (o *Options) now() time.Time {
	if o.Now != nil {
		return o.Now().UTC()
	}
	return time.Now().UTC()
}

// Create captures one backup set and publishes it atomically. On any error no
// set is promoted and the staging directory is removed.
func Create(ctx context.Context, o Options) (res Result, err error) {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	if err = o.prepare(); err != nil {
		return res, err
	}
	started := o.now()
	fence, err := o.acquireFence(ctx)
	if err != nil {
		return res, err
	}
	defer func() { _ = fence.Release() }()

	o.cleanStale(log)
	if err = o.reserve(ctx, 0); err != nil {
		return res, err
	}
	setID, err := newSetID(started, o.Mode)
	if err != nil {
		return res, err
	}
	stagingRoot := filepath.Join(o.Root, StagingDir)
	stage := filepath.Join(stagingRoot, setID)
	release, err := o.openStage(stagingRoot, stage)
	if err != nil {
		return res, err
	}
	defer release()
	promoted := false
	defer func() {
		if !promoted {
			_ = os.RemoveAll(stage)
		}
	}()

	m := Manifest{
		FormatVersion: FormatVersion, SetID: setID, Mode: o.Mode,
		StartedAt: started.Format(time.RFC3339), GopodVersion: o.Version,
		Coverage: coverage(o.Mode), NotCovered: notCovered(o.Mode),
		Files: []FileEntry{}, Artifacts: []store.BackupArtifact{},
	}
	if fence != nil {
		m.Fence = &FenceRecord{ControllerLock: true, ContainerEvidence: fence.ContainerEvidence}
	}
	if err = o.capture(ctx, stage, &m); err != nil {
		return res, err
	}
	if err = o.seal(stage, stagingRoot, &m); err != nil {
		return res, err
	}
	final := filepath.Join(o.Root, setID)
	if err = o.fault("publish.rename"); err != nil {
		return res, err
	}
	if err = os.Rename(stage, final); err != nil {
		return res, fmt.Errorf("backup: publish: %w", err)
	}
	promoted = true
	res = Result{SetID: setID, Path: final, Manifest: m}
	if err = o.fault("root.dirsync"); err != nil {
		// The set is complete and verifiable but its directory entry may not be
		// durable yet; report the failure so the operator re-verifies.
		return res, err
	}
	if err = syncDir(o.Root); err != nil {
		return res, fmt.Errorf("backup: fsync root: %w", err)
	}
	log.Info("backup published", slog.String("set", setID), slog.String("mode", o.Mode),
		slog.Int64("db_bytes", m.Database.Size), slog.Int("files", len(m.Files)))
	return res, nil
}

// prepare validates the options, applies defaults and checks the root.
func (o *Options) prepare() error {
	if o.Mode != ModeDatabaseOnly && o.Mode != ModeMatchedStopped {
		return fmt.Errorf("backup: unknown mode %q", o.Mode)
	}
	if o.StorePath == "" {
		return errors.New("backup: store path is required")
	}
	if o.MaxArtifactBytes <= 0 {
		o.MaxArtifactBytes = DefaultMaxArtifactBytes
	}
	if o.FreeBytes == nil {
		o.FreeBytes = freeBytes
	}
	if o.StagingMaxAge <= 0 {
		o.StagingMaxAge = 30 * time.Minute
	}
	forbidden := append([]string{filepath.Dir(o.StorePath), o.ArtifactsDir}, o.Forbidden...)
	forbidden = append(forbidden, o.ConfigFiles...)
	return ValidateRoot(o.Root, forbidden)
}

// acquireFence takes the stopped-writer fence for matched_stopped; it returns
// a nil fence (safe to Release) for database_only.
func (o *Options) acquireFence(ctx context.Context) (*maintenance.Fence, error) {
	if o.Mode != ModeMatchedStopped {
		return nil, nil
	}
	if o.Settlement == nil {
		return nil, errors.New("backup: matched_stopped requires a settlement check")
	}
	return o.Settlement.Acquire(ctx)
}

func (o *Options) cleanStale(log *slog.Logger) {
	n, err := CleanStaging(o.Root, o.StagingMaxAge, o.now())
	switch {
	case err != nil:
		log.Warn("backup: stale staging cleanup incomplete", slog.Any("err", err))
	case n > 0:
		log.Info("backup: removed stale staging entries", slog.Int("count", n))
	}
}

// openStage creates the staging directory under a per-capture lock kept beside
// it (not inside, so it never becomes part of the set) which marks the
// directory live for CleanStaging. The returned func drops the lock.
func (o *Options) openStage(stagingRoot, stage string) (func(), error) {
	if err := o.fault("stage.mkdir"); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(stagingRoot, 0o700); err != nil {
		return nil, fmt.Errorf("backup: create staging: %w", err)
	}
	captureLock, err := maintenance.TryLock(stage + stageLockSuffix)
	if err != nil {
		return nil, fmt.Errorf("backup: capture lock: %w", err)
	}
	release := func() {
		_ = os.Remove(stage + stageLockSuffix)
		_ = captureLock.Release()
	}
	if err := os.Mkdir(stage, 0o700); err != nil {
		release()
		return nil, fmt.Errorf("backup: create staging set: %w", err)
	}
	return release, nil
}

// capture snapshots the database and, for matched_stopped, copies the declared
// config files and referenced artifact blobs into the staging directory.
func (o *Options) capture(ctx context.Context, stage string, m *Manifest) error {
	if err := o.fault("snapshot"); err != nil {
		return err
	}
	dbPath := filepath.Join(stage, DBName)
	info, err := store.SnapshotFile(ctx, o.StorePath, dbPath, store.SnapshotOptions{
		Fault:                     o.Fault,
		AllowForeignKeyViolations: o.Mode == ModeDatabaseOnly,
	})
	if err != nil {
		return fmt.Errorf("backup: database snapshot: %w", err)
	}
	m.Database = DatabaseEntry{
		FileEntry:           FileEntry{Role: RoleDatabase, Path: DBName, Size: info.Bytes, SHA256: info.SHA256},
		SQLiteSchemaVersion: info.SQLiteSchemaVersion, SchemaFingerprint: info.SchemaFingerprint,
		IntegrityOK: info.IntegrityOK, Tables: info.Tables,
	}
	if len(info.ForeignKeyViolations) > 0 {
		m.Database.ForeignKeyViolations = info.ForeignKeyViolations
		m.Warnings = append(m.Warnings, "source database already had foreign key violations (orphan rows) in: "+store.FormatViolations(info.ForeignKeyViolations)+"; they were copied as found")
	}
	if m.Work, err = store.ReadWorkEvidence(ctx, dbPath); err != nil {
		return err
	}
	if o.Mode != ModeMatchedStopped {
		return nil
	}
	if err = o.copyConfig(stage, m); err != nil {
		return err
	}
	return o.copyArtifacts(ctx, dbPath, stage, m)
}

// seal writes the manifest and completion marker and fsyncs the staging tree.
func (o *Options) seal(stage, stagingRoot string, m *Manifest) error {
	m.CompletedAt = o.now().Format(time.RFC3339)
	manifest, marker, err := marshalManifest(*m)
	if err != nil {
		return err
	}
	if err = o.writeFile(filepath.Join(stage, ManifestName), manifest, "manifest.write", "manifest.sync"); err != nil {
		return err
	}
	if err = o.writeFile(filepath.Join(stage, MarkerName), marker, "marker.write", "marker.sync"); err != nil {
		return err
	}
	if err = o.fault("stage.dirsync"); err != nil {
		return err
	}
	if err = syncTree(stage); err != nil {
		return fmt.Errorf("backup: fsync staging: %w", err)
	}
	if err = syncDir(stagingRoot); err != nil {
		return fmt.Errorf("backup: fsync staging root: %w", err)
	}
	return nil
}

func newSetID(t time.Time, mode string) (string, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("backup: set id: %w", err)
	}
	tag := "db"
	if mode == ModeMatchedStopped {
		tag = "ms"
	}
	return t.Format("20060102T150405Z") + "-" + tag + "-" + hex.EncodeToString(b[:]), nil
}

// reserve fails closed when the cap or free space cannot hold extra more bytes
// plus the database (counted at twice its size: copy plus integrity work).
func (o *Options) reserve(_ context.Context, extra int64) error {
	var need int64 = extra
	if fi, err := os.Stat(o.StorePath); err == nil {
		need += 2 * fi.Size()
		if wi, werr := os.Stat(o.StorePath + "-wal"); werr == nil {
			need += wi.Size()
		}
	} else {
		return fmt.Errorf("backup: store: %w", err)
	}
	if o.MaxBytes > 0 {
		used, err := usedBytes(o.Root)
		if err != nil {
			return err
		}
		if used+need > o.MaxBytes {
			return fmt.Errorf("%w: %d used + %d needed exceeds cap %d", ErrQuota, used, need, o.MaxBytes)
		}
	}
	free, err := o.FreeBytes(o.Root)
	if err != nil {
		return fmt.Errorf("backup: free space: %w", err)
	}
	if free < uint64(need) {
		return fmt.Errorf("%w: %d bytes free, %d needed", ErrQuota, free, need)
	}
	return nil
}

func usedBytes(root string) (int64, error) {
	var total int64
	err := filepath.WalkDir(root, func(_ string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && d.Name() == StagingDir {
			return filepath.SkipDir // stale or live staging is not set storage
		}
		if d.Type().IsRegular() {
			fi, ierr := d.Info()
			if ierr != nil {
				return ierr
			}
			total += fi.Size()
		}
		return nil
	})
	return total, err
}

// writeFile writes data 0600 exclusively, fsyncs it, with fault points.
func (o *Options) writeFile(p string, data []byte, writePoint, syncPoint string) error {
	if err := o.fault(writePoint); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("backup: create %s: %w", filepath.Base(p), err)
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return fmt.Errorf("backup: write %s: %w", filepath.Base(p), err)
	}
	if err := o.fault(syncPoint); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return fmt.Errorf("backup: fsync %s: %w", filepath.Base(p), err)
	}
	return f.Close()
}

// copyConfig copies declared regular files without following symlinks and
// rejects a source that changed while it was read.
func (o *Options) copyConfig(stage string, m *Manifest) error {
	seen := map[string]bool{}
	if len(o.ConfigFiles) == 0 {
		return nil
	}
	if err := os.Mkdir(filepath.Join(stage, ConfigDir), 0o700); err != nil {
		return fmt.Errorf("backup: create config dir: %w", err)
	}
	for _, src := range o.ConfigFiles {
		name := filepath.Base(src)
		if seen[name] || name == "." || name == string(filepath.Separator) {
			return fmt.Errorf("backup: config file name %q is duplicated or invalid", name)
		}
		seen[name] = true
		data, err := readNoFollow(src, 64<<20)
		if err != nil {
			return fmt.Errorf("backup: config %s: %w", name, err)
		}
		rel := path.Join(ConfigDir, name)
		if err := o.writeFile(filepath.Join(stage, filepath.FromSlash(rel)), data, faultFileWrite, faultFileSync); err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		m.Files = append(m.Files, FileEntry{Role: RoleConfig, Path: rel, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])})
	}
	return nil
}

// readNoFollow reads a regular file (never through a symlink or hardlink-sized
// surprise) and fails when its size or mtime changed while reading.
func readNoFollow(p string, limit int64) ([]byte, error) {
	fd, err := unix.Open(p, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), p)
	defer f.Close()
	var before unix.Stat_t
	if err := unix.Fstat(fd, &before); err != nil {
		return nil, err
	}
	if before.Mode&unix.S_IFMT != unix.S_IFREG {
		return nil, errors.New("not a regular file")
	}
	if before.Size > limit {
		return nil, errors.New("file too large")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, err
	}
	if after.Size != before.Size || after.Mtim != before.Mtim || int64(len(data)) != before.Size {
		return nil, ErrSourceChanged
	}
	return data, nil
}

// copyArtifacts copies the verified bytes of every ready artifact referenced
// by the snapshot database. A ready artifact without a matching blob fails the
// capture: a set that cannot restore its references is not published.
func (o *Options) copyArtifacts(ctx context.Context, dbPath, stage string, m *Manifest) error {
	inv, err := store.ArtifactInventory(ctx, dbPath)
	if err != nil {
		return err
	}
	m.Artifacts = append(m.Artifacts, inv...)
	if len(inv) == 0 {
		return nil
	}
	var total int64
	for _, a := range inv {
		total += a.Size
	}
	if err := o.reserve(ctx, total); err != nil {
		return err
	}
	var blobs *artifactfs.Blobs
	if _, serr := os.Stat(o.ArtifactsDir); serr == nil {
		if blobs, err = artifactfs.OpenBlobs(o.ArtifactsDir); err != nil {
			return fmt.Errorf("backup: open artifact store: %w", err)
		}
		defer blobs.Close()
	}
	sort.Slice(inv, func(i, j int) bool { return inv[i].BlobKey < inv[j].BlobKey })
	for _, a := range inv {
		var data []byte
		if blobs != nil && a.Size <= o.MaxArtifactBytes {
			data, err = blobs.ReadVerified(a.BlobKey, a.Size, a.SHA256, o.MaxArtifactBytes)
		} else {
			err = artifactfs.ErrNotFound
		}
		if err != nil {
			if a.State == store.ArtifactQuarantined {
				continue // quarantined bytes are optional; the inventory row is kept
			}
			return fmt.Errorf("%w: %s (%v)", ErrArtifactMissing, a.ID, err)
		}
		rel := path.Join(ArtifactDir, a.BlobKey)
		full := filepath.Join(stage, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			return fmt.Errorf("backup: create artifact dir: %w", err)
		}
		if err := o.writeFile(full, data, faultFileWrite, faultFileSync); err != nil {
			return err
		}
		m.Files = append(m.Files, FileEntry{Role: RoleArtifact, Path: rel, Size: a.Size, SHA256: a.SHA256})
	}
	return nil
}

// syncTree fsyncs every directory under root (bottom up) and root itself.
func syncTree(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := syncDir(dirs[i]); err != nil {
			return err
		}
	}
	return nil
}

const stageLockSuffix = ".lock"

// CleanStaging removes leftovers of crashed captures from <root>/.staging:
// entries older than maxAge whose per-capture lock is free. A live capture
// holds its lock, so its directory is never removed, however old. It returns
// the number of removed staging directories.
func CleanStaging(root string, maxAge time.Duration, now time.Time) (int, error) {
	dir := filepath.Join(root, StagingDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	removed := 0
	var firstErr error
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), stageLockSuffix) {
			continue // handled with its directory, or below when orphaned
		}
		ok, rerr := removeIfStale(dir, e, maxAge, now)
		if rerr != nil && firstErr == nil {
			firstErr = rerr
		}
		if ok {
			removed++
		}
	}
	removeOrphanLocks(dir, entries)
	return removed, firstErr
}

// removeIfStale removes one staging entry that is old enough and whose
// per-capture lock is free. It reports whether it removed the directory.
func removeIfStale(dir string, e os.DirEntry, maxAge time.Duration, now time.Time) (bool, error) {
	info, err := e.Info()
	if err != nil || now.Sub(info.ModTime()) < maxAge {
		return false, nil
	}
	lockPath := filepath.Join(dir, e.Name()+stageLockSuffix)
	l, err := maintenance.TryLock(lockPath)
	if err != nil {
		return false, nil // held: a capture is running
	}
	defer func() { _ = l.Release() }()
	rerr := os.RemoveAll(filepath.Join(dir, e.Name()))
	_ = os.Remove(lockPath)
	return rerr == nil, rerr
}

// removeOrphanLocks deletes lock files whose staging directory is gone.
func removeOrphanLocks(dir string, entries []os.DirEntry) {
	for _, e := range entries {
		name := e.Name()
		if !strings.HasSuffix(name, stageLockSuffix) {
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, strings.TrimSuffix(name, stageLockSuffix))); !errors.Is(err, os.ErrNotExist) {
			continue
		}
		if l, err := maintenance.TryLock(filepath.Join(dir, name)); err == nil {
			_ = os.Remove(filepath.Join(dir, name))
			_ = l.Release()
		}
	}
}
