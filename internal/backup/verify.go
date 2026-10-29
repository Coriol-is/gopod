package backup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/sys/unix"

	"github.com/Coriol-is/gopod/internal/store"
)

// Report is the outcome of an offline restore validation. It carries counts
// and identifiers only, never file contents or secrets.
type Report struct {
	SetID      string   `json:"set_id"`
	Mode       string   `json:"mode"`
	OK         bool     `json:"ok"`
	Problems   []string `json:"problems"`
	Warnings   []string `json:"warnings"`
	Coverage   []string `json:"coverage"`
	NotCovered []string `json:"not_covered"`
	Files      int      `json:"files_checked"`
	Tables     int      `json:"tables"`
	Rows       int64    `json:"rows"`
	// Unsettled is the work evidence recorded at capture time: rows that a
	// restored instance must hold as quarantined evidence, never replay.
	Unsettled bool `json:"unsettled_work_recorded"`
	// Notice states what the check proves: the manifest hash is unkeyed.
	Notice string `json:"notice"`
}

// IntegrityNotice is printed with every verification result.
const IntegrityNotice = "Hashes are unkeyed: they detect corruption and accidental change, not tampering or authenticity. Anyone who can rewrite a whole set can rewrite its hashes."

// VerifyOptions tune Verify.
type VerifyOptions struct {
	// ScratchDir holds the scratch restore of the database (about 2-3 times its
	// size, plus one empty store for the schema reference). Empty means
	// <backup root>/.staging, falling back to the system temp directory only
	// when the root is not writable.
	ScratchDir string
}

type verifier struct {
	rep         *Report
	scratch     string
	setPath     string
	schemaNewer error
}

func (v *verifier) problem(format string, a ...any) {
	v.rep.Problems = append(v.rep.Problems, fmt.Sprintf(format, a...))
}

// Verify validates the set directory at setPath without touching it and
// without starting the application: manifest and marker, every file hash,
// unlisted files, then a scratch restore of the database checked read-only
// (integrity, foreign keys, fingerprint, row counts, schema compatibility
// with this binary) and the artifact references against the stored blobs.
// The report lists every problem found; the returned error wraps the first.
func Verify(ctx context.Context, setPath string) (Report, error) {
	return VerifyWith(ctx, setPath, VerifyOptions{})
}

// VerifyWith is Verify with options.
func VerifyWith(ctx context.Context, setPath string, opt VerifyOptions) (Report, error) {
	rep := Report{Problems: []string{}, Warnings: []string{}, Coverage: []string{}, NotCovered: []string{}, Notice: IntegrityNotice}
	v := &verifier{rep: &rep, scratch: opt.ScratchDir, setPath: setPath}
	m, hard, ok := v.loadManifest()
	if hard != nil {
		return rep, hard
	}
	if !ok {
		return v.finish()
	}
	v.checkIdentity(m)
	listed := v.listFiles(m)
	for p, f := range listed {
		if !validRelPath(p) {
			continue
		}
		if err := checkFile(setPath, f); err != nil {
			v.problem(msgPathErr, p, err)
		}
		rep.Files++
	}
	v.checkUnlisted(setPath, listed)
	if !hasProblem(rep, DBName) {
		v.checkDatabase(ctx, setPath, m)
	}
	v.checkArtifacts(m, listed)
	return v.finish()
}

// msgPathErr formats "<name>: <error>" problems.
const msgPathErr = "%s: %v"

func (v *verifier) finish() (Report, error) {
	v.rep.OK = len(v.rep.Problems) == 0
	if v.rep.OK {
		return *v.rep, nil
	}
	if v.schemaNewer != nil {
		return *v.rep, fmt.Errorf("%w: %w", ErrInvalidSet, v.schemaNewer)
	}
	return *v.rep, fmt.Errorf("%w: %s", ErrInvalidSet, v.rep.Problems[0])
}

// loadManifest reads and authenticates the manifest and marker. ok is false
// when verification cannot continue (the problem is recorded); hard is a
// non-report error (unsupported future format).
func (v *verifier) loadManifest() (m Manifest, hard error, ok bool) {
	fi, err := os.Lstat(v.setPath)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		v.problem("set path is not a plain directory")
		return m, nil, false
	}
	if fi.Mode().Perm()&0o077 != 0 {
		v.rep.Warnings = append(v.rep.Warnings, "set directory is accessible by group or others (expected 0700)")
	}
	raw, err := readSmall(filepath.Join(v.setPath, ManifestName))
	if err != nil {
		v.problem("manifest unreadable")
		return m, nil, false
	}
	marker, err := readSmall(filepath.Join(v.setPath, MarkerName))
	if err != nil {
		v.problem("completion marker missing: the capture never finished")
		return m, nil, false
	}
	if !bytes.Equal(marker, markerFor(raw)) {
		v.problem("completion marker does not match the manifest (tampered or truncated)")
		return m, nil, false
	}
	m, err = parseManifest(raw)
	if err != nil {
		if errors.Is(err, ErrUnsupportedFormat) {
			return m, err, false
		}
		v.problem("%v", err)
		return m, nil, false
	}
	return m, nil, true
}

// checkIdentity records the set facts in the report and checks id and mode.
func (v *verifier) checkIdentity(m Manifest) {
	v.rep.SetID, v.rep.Mode, v.rep.Coverage, v.rep.NotCovered = m.SetID, m.Mode, m.Coverage, m.NotCovered
	v.rep.Unsettled = m.Work.Unsettled()
	if !setIDPattern.MatchString(m.SetID) || m.SetID != filepath.Base(v.setPath) {
		v.problem("set id does not match its directory")
	}
	if m.Mode != ModeDatabaseOnly && m.Mode != ModeMatchedStopped {
		v.problem("unknown mode %q", m.Mode)
	}
}

// listFiles indexes the database and listed files by path, reporting an
// unexpected database entry, unsafe paths and duplicates.
func (v *verifier) listFiles(m Manifest) map[string]FileEntry {
	listed := map[string]FileEntry{DBName: m.Database.FileEntry}
	if m.Database.Path != DBName || m.Database.Role != RoleDatabase {
		v.problem("database entry has an unexpected path or role")
	}
	for _, f := range m.Files {
		if !validRelPath(f.Path) {
			v.problem("unsafe path in manifest")
			continue
		}
		if _, dup := listed[f.Path]; dup {
			v.problem("duplicate manifest path %s", f.Path)
			continue
		}
		listed[f.Path] = f
	}
	return listed
}

func hasProblem(rep Report, prefix string) bool {
	for _, p := range rep.Problems {
		if len(p) >= len(prefix) && p[:len(prefix)] == prefix {
			return true
		}
	}
	return false
}

func readSmall(p string) ([]byte, error) {
	f, err := os.OpenFile(p, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(io.LimitReader(f, 64<<20))
}

// checkFile verifies a listed file is a regular, non-symlink file with the
// recorded size and hash.
func checkFile(setPath string, f FileEntry) error {
	file, err := openNoSymlinks(setPath, f.Path)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return errors.New("missing")
		}
		return errors.New("not readable as a regular file (a symlinked file or directory in the path?)")
	}
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
		return errors.New("not a regular file")
	}
	if st.Size != f.Size {
		return fmt.Errorf("size %d does not match manifest %d", st.Size, f.Size)
	}
	h := sha256.New()
	if _, err := io.Copy(h, file); err != nil {
		return errors.New("read error")
	}
	if hex.EncodeToString(h.Sum(nil)) != f.SHA256 {
		return errors.New("hash mismatch")
	}
	return nil
}

// openNoSymlinks opens rel below setPath one component at a time with
// O_NOFOLLOW (openat), so a symlink in any parent directory, not only in the
// final name, is refused. It works the same on Linux and macOS.
func openNoSymlinks(setPath, rel string) (*os.File, error) {
	fd, err := unix.Open(setPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(rel, "/")
	for i, part := range parts {
		flags := unix.O_RDONLY | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if i < len(parts)-1 {
			flags |= unix.O_DIRECTORY
		} else {
			flags |= unix.O_NONBLOCK
		}
		next, err := unix.Openat(fd, part, flags, 0)
		_ = unix.Close(fd)
		if err != nil {
			return nil, err
		}
		fd = next
	}
	return os.NewFile(uintptr(fd), rel), nil
}

func (v *verifier) checkUnlisted(setPath string, listed map[string]FileEntry) {
	_ = filepath.WalkDir(setPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		rel, rerr := filepath.Rel(setPath, p)
		if rerr != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if rel == ManifestName || rel == MarkerName {
			return nil
		}
		if _, ok := listed[rel]; !ok {
			v.problem("unlisted file %s", rel)
		}
		return nil
	})
}

// checkDatabase restores the database into a fresh private scratch directory
// and inspects the copy read-only; Store.Open (which migrates) is never used.
func (v *verifier) checkDatabase(ctx context.Context, setPath string, m Manifest) {
	scratch, err := v.makeScratch()
	if err != nil {
		v.problem("cannot create scratch directory: %v", err)
		return
	}
	defer os.RemoveAll(scratch)
	copyPath := filepath.Join(scratch, DBName)
	if err := copyPrivate(filepath.Join(setPath, DBName), copyPath); err != nil {
		v.problem("%s: scratch restore failed", DBName)
		return
	}
	info, err := store.InspectDB(ctx, copyPath)
	if err != nil {
		v.problem(msgPathErr, DBName, err)
		return
	}
	for _, name := range info.UnsafeTables {
		v.problem("%s: foreign or damaged table name that is not a plain identifier (%.40q); it was not queried", DBName, name)
	}
	if info.SchemaFingerprint != m.Database.SchemaFingerprint {
		v.problem("%s: schema fingerprint differs from the manifest", DBName)
	}
	if !tablesEqual(info.Tables, m.Database.Tables) {
		v.problem("%s: row counts differ from the manifest", DBName)
	}
	if !tablesEqual(info.ForeignKeyViolations, m.Database.ForeignKeyViolations) {
		v.problem("%s: foreign key violations differ from the manifest", DBName)
	} else if len(info.ForeignKeyViolations) > 0 {
		if m.Mode == ModeMatchedStopped {
			v.problem("%s: foreign key violations in a matched_stopped set (%s)", DBName, store.FormatViolations(info.ForeignKeyViolations))
		} else {
			v.rep.Warnings = append(v.rep.Warnings, "database has pre-existing foreign key violations (orphan rows): "+store.FormatViolations(info.ForeignKeyViolations))
		}
	}
	v.rep.Tables = len(info.Tables)
	for _, n := range info.Tables {
		v.rep.Rows += n
	}
	if err := store.CheckSchemaCompatIn(ctx, copyPath, filepath.Dir(scratch)); err != nil {
		if errors.Is(err, store.ErrSchemaNewer) {
			v.schemaNewer = err
		}
		v.problem(msgPathErr, DBName, err)
	}
	inv, err := store.ArtifactInventory(ctx, copyPath)
	if err != nil {
		v.problem("%s: artifact inventory unreadable", DBName)
		return
	}
	if !inventoryEqual(inv, m.Artifacts) {
		v.problem("artifact inventory in the manifest differs from the database")
	}
}

func copyPrivate(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func tablesEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if bv, ok := b[k]; !ok || bv != v {
			return false
		}
	}
	return true
}

func inventoryEqual(a, b []store.BackupArtifact) bool {
	if len(a) != len(b) {
		return false
	}
	key := func(x []store.BackupArtifact) []string {
		out := make([]string, len(x))
		for i, e := range x {
			out[i] = fmt.Sprintf("%s|%s|%s|%d|%s", e.ID, e.State, e.BlobKey, e.Size, e.SHA256)
		}
		sort.Strings(out)
		return out
	}
	ka, kb := key(a), key(b)
	for i := range ka {
		if ka[i] != kb[i] {
			return false
		}
	}
	return true
}

// checkArtifacts matches database artifact references to the blobs in the set.
func (v *verifier) checkArtifacts(m Manifest, listed map[string]FileEntry) {
	ready := 0
	for _, a := range m.Artifacts {
		if a.State != store.ArtifactReady {
			continue
		}
		ready++
		if m.Mode != ModeMatchedStopped {
			continue
		}
		f, ok := listed[path.Join(ArtifactDir, a.BlobKey)]
		if !ok {
			v.problem("artifact %s is referenced by the database but its blob is not in the set", a.ID)
			continue
		}
		if f.Role != RoleArtifact || f.Size != a.Size || f.SHA256 != a.SHA256 {
			v.problem("artifact %s blob does not match its inventory row", a.ID)
		}
	}
	if m.Mode == ModeDatabaseOnly && ready > 0 {
		v.rep.Warnings = append(v.rep.Warnings, fmt.Sprintf("database references %d ready artifact(s) whose bytes are not part of this database_only set", ready))
	}
}

// makeScratch creates a private scratch directory under the configured parent,
// by default <backup root>/.staging, so a small /tmp (tmpfs on a Pi) is not
// the limit. The returned directory's parent is what CheckSchemaCompatIn uses.
func (v *verifier) makeScratch() (string, error) {
	parent := v.scratch
	if parent == "" {
		parent = filepath.Join(filepath.Dir(v.setPath), StagingDir)
		if err := os.MkdirAll(parent, 0o700); err != nil {
			parent = "" // read-only root: fall back to the system temp dir
		}
	} else if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(parent, "verify-")
}
