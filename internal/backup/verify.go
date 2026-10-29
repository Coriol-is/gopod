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
}

type verifier struct {
	rep         *Report
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
	rep := Report{Problems: []string{}, Warnings: []string{}, Coverage: []string{}, NotCovered: []string{}}
	v := &verifier{rep: &rep}
	finish := func() (Report, error) {
		rep.OK = len(rep.Problems) == 0
		if rep.OK {
			return rep, nil
		}
		if v.schemaNewer != nil {
			return rep, fmt.Errorf("%w: %w", ErrInvalidSet, v.schemaNewer)
		}
		return rep, fmt.Errorf("%w: %s", ErrInvalidSet, rep.Problems[0])
	}

	fi, err := os.Lstat(setPath)
	if err != nil || !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
		v.problem("set path is not a plain directory")
		return finish()
	}
	if fi.Mode().Perm()&0o077 != 0 {
		rep.Warnings = append(rep.Warnings, "set directory is accessible by group or others (expected 0700)")
	}
	raw, err := readSmall(filepath.Join(setPath, ManifestName))
	if err != nil {
		v.problem("manifest unreadable")
		return finish()
	}
	marker, err := readSmall(filepath.Join(setPath, MarkerName))
	if err != nil {
		v.problem("completion marker missing: the capture never finished")
		return finish()
	}
	if !bytes.Equal(marker, markerFor(raw)) {
		v.problem("completion marker does not match the manifest (tampered or truncated)")
		return finish()
	}
	m, err := parseManifest(raw)
	if err != nil {
		if errors.Is(err, ErrUnsupportedFormat) {
			return rep, err
		}
		v.problem("%v", err)
		return finish()
	}
	rep.SetID, rep.Mode, rep.Coverage, rep.NotCovered = m.SetID, m.Mode, m.Coverage, m.NotCovered
	rep.Unsettled = m.Work.Unsettled()
	if !setIDPattern.MatchString(m.SetID) || m.SetID != filepath.Base(setPath) {
		v.problem("set id does not match its directory")
	}
	if m.Mode != ModeDatabaseOnly && m.Mode != ModeMatchedStopped {
		v.problem("unknown mode %q", m.Mode)
	}

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
	for p, f := range listed {
		if !validRelPath(p) {
			continue
		}
		if err := checkFile(setPath, f); err != nil {
			v.problem("%s: %v", p, err)
		}
		rep.Files++
	}
	v.checkUnlisted(setPath, listed)

	if !hasProblem(rep, DBName) {
		v.checkDatabase(ctx, setPath, m)
	}
	v.checkArtifacts(m, listed)
	return finish()
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
	full := filepath.Join(setPath, filepath.FromSlash(f.Path))
	fd, err := unix.Open(full, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
	if err != nil {
		if errors.Is(err, unix.ENOENT) {
			return errors.New("missing")
		}
		return errors.New("not readable as a regular file (symlink?)")
	}
	file := os.NewFile(uintptr(fd), full)
	defer file.Close()
	var st unix.Stat_t
	if err := unix.Fstat(fd, &st); err != nil || st.Mode&unix.S_IFMT != unix.S_IFREG {
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
	scratch, err := os.MkdirTemp("", "gopod-restore-check-")
	if err != nil {
		v.problem("cannot create scratch directory")
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
		v.problem("%s: %v", DBName, err)
		return
	}
	if info.SchemaFingerprint != m.Database.SchemaFingerprint {
		v.problem("%s: schema fingerprint differs from the manifest", DBName)
	}
	if !tablesEqual(info.Tables, m.Database.Tables) {
		v.problem("%s: row counts differ from the manifest", DBName)
	}
	v.rep.Tables = len(info.Tables)
	for _, n := range info.Tables {
		v.rep.Rows += n
	}
	if err := store.CheckSchemaCompat(ctx, copyPath); err != nil {
		if errors.Is(err, store.ErrSchemaNewer) {
			v.schemaNewer = err
		}
		v.problem("%s: %v", DBName, err)
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
