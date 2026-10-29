package backup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

const (
	testPrep  = "0a1b2c3d-4e5f-4a6b-8c7d-8e9f0a1b2c3d"
	testBlob  = "11111111-2222-4333-8444-555555555555.blob"
	testBlob2 = "66666666-7777-4888-9999-aaaaaaaaaaaa.blob"
)

type env struct {
	t         *testing.T
	dir       string // data dir
	root      string // backup root, outside the data dir
	storePath string
	artifacts string
	cfgFile   string
	st        *store.Store
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base := t.TempDir()
	e := &env{t: t, dir: filepath.Join(base, "data"), root: filepath.Join(base, "backups")}
	if err := os.MkdirAll(e.dir, 0o755); err != nil {
		t.Fatal(err)
	}
	e.storePath = filepath.Join(e.dir, "store.sqlite")
	e.artifacts = filepath.Join(e.dir, "artifacts")
	st, err := store.Open(context.Background(), e.storePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	e.st = st
	for i := 0; i < 25; i++ {
		if err := st.SetState(context.Background(), fmt.Sprintf("k%d", i), "v"); err != nil {
			t.Fatal(err)
		}
	}
	e.cfgFile = filepath.Join(e.dir, "mount-allowlist.json")
	if err := os.WriteFile(e.cfgFile, []byte(`{"extra_mounts":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	return e
}

// addArtifact inserts a ready inventory row and, when withBlob, its blob.
func (e *env) addArtifact(id, name, content string, withBlob bool) {
	e.t.Helper()
	sum := sha256.Sum256([]byte(content))
	sha := hex.EncodeToString(sum[:])
	key := testPrep + "/" + name
	db := e.st.DB()
	now := time.Now().UnixMilli()
	_, _ = db.Exec(`INSERT OR IGNORE INTO artifact_reservations(preparation_id,namespace_id,namespace_epoch,bytes,file_count,state,created_at,expires_at)
 VALUES(?,?,?,?,?,?,?,?)`, testPrep, "ns", 1, 10, 2, "released", now, now+1000)
	if _, err := db.Exec(`INSERT INTO artifacts(artifact_id,namespace_id,namespace_epoch,state,relative_blob_key,sha256,size,created_at,preparation_id)
 VALUES(?,?,?,?,?,?,?,?,?)`, id, "ns", 1, "ready", key, sha, len(content), now, testPrep); err != nil {
		e.t.Fatal(err)
	}
	if withBlob {
		dir := filepath.Join(e.artifacts, testPrep)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			e.t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			e.t.Fatal(err)
		}
	}
}

func (e *env) opts(mode string) Options {
	o := Options{
		Root: e.root, Mode: mode, StorePath: e.storePath, ArtifactsDir: e.artifacts,
		Version: "test", Log: nil,
	}
	if mode == ModeMatchedStopped {
		o.ConfigFiles = []string{e.cfgFile}
		o.Settlement = &maintenance.Settlement{LockPath: maintenance.ControllerLockPath(e.dir), AssertNoContainers: true}
	}
	return o
}

func publishedSets(t *testing.T, root string) []string {
	t.Helper()
	l, err := List(root)
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, s := range l {
		ids = append(ids, s.SetID)
	}
	return ids
}

// resign rewrites the manifest through mutate and recomputes the marker, as a
// careful adversary or a buggy tool would; verify must still catch it.
func resign(t *testing.T, setDir string, mutate func(*Manifest)) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(setDir, ManifestName))
	if err != nil {
		t.Fatal(err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatal(err)
	}
	mutate(&m)
	man, mark, err := marshalManifest(m)
	if err != nil {
		t.Fatal(err)
	}
	for name, b := range map[string][]byte{ManifestName: man, MarkerName: mark} {
		if err := os.WriteFile(filepath.Join(setDir, name), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func fileHash(t *testing.T, p string) (string, int64) {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:]), int64(len(b))
}

func copyDir(t *testing.T, from, to string) {
	t.Helper()
	err := filepath.WalkDir(from, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(from, p)
		dst := filepath.Join(to, rel)
		if d.IsDir() {
			return os.MkdirAll(dst, 0o700)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(dst, b, 0o600)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func execSQL(t *testing.T, dbPath, stmt string) {
	t.Helper()
	st, err := store.Open(context.Background(), dbPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(stmt); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
}

func dirDigest(t *testing.T, dir string) string {
	t.Helper()
	h := sha256.New()
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		fi, _ := d.Info()
		fmt.Fprintf(h, "%s|%v|%d|%v\n", p, d.IsDir(), fi.Size(), fi.ModTime().UnixNano())
		if !d.IsDir() {
			b, _ := os.ReadFile(p)
			h.Write(b)
		}
		return nil
	})
	return hex.EncodeToString(h.Sum(nil))
}
