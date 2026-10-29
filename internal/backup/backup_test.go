package backup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestCreateVerifyRestoreRoundTrip(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	res, err := Create(ctx, e.opts(ModeDatabaseOnly))
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	rep, err := Verify(ctx, res.Path)
	if err != nil || !rep.OK {
		t.Fatalf("verify: %v %+v", err, rep)
	}
	if rep.Mode != ModeDatabaseOnly || len(rep.NotCovered) == 0 || rep.Rows == 0 {
		t.Fatalf("report %+v", rep)
	}
	// Restore into a scratch location and open with the real Store.
	restored := filepath.Join(t.TempDir(), "store.sqlite")
	b, _ := os.ReadFile(filepath.Join(res.Path, DBName))
	if err := os.WriteFile(restored, b, 0o600); err != nil {
		t.Fatal(err)
	}
	rs, err := store.Open(ctx, restored, nil)
	if err != nil {
		t.Fatalf("open restored: %v", err)
	}
	defer rs.Close()
	for i := 0; i < 25; i++ {
		v, err := rs.GetState(ctx, fmt.Sprintf("k%d", i))
		if err != nil || v != "v" {
			t.Fatalf("restored row k%d = %q, %v", i, v, err)
		}
	}
	l, _ := List(e.root)
	if len(l) != 1 || !l[0].Sealed || l[0].SetID != res.SetID {
		t.Fatalf("list %+v", l)
	}
}

func TestPermissionsArePrivate(t *testing.T) {
	e := newEnv(t)
	e.addArtifact("art-1", testBlob, "hello", true)
	if _, err := Create(context.Background(), e.opts(ModeMatchedStopped)); err != nil {
		t.Fatal(err)
	}
	err := filepath.WalkDir(e.root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		fi, _ := d.Info()
		want := os.FileMode(0o600)
		if d.IsDir() {
			want = 0o700
		}
		if fi.Mode().Perm() != want {
			t.Errorf("%s mode %v want %v", p, fi.Mode().Perm(), want)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestCreateWithConcurrentWriterVerifies(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	var stop atomic.Bool
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			_ = e.st.SetState(ctx, fmt.Sprintf("live%d", i), "x")
		}
	}()
	c, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	res, err := Create(c, e.opts(ModeDatabaseOnly))
	stop.Store(true)
	wg.Wait()
	if err != nil {
		t.Fatal(err)
	}
	if rep, err := Verify(ctx, res.Path); err != nil || !rep.OK {
		t.Fatalf("verify: %v %+v", err, rep)
	}
}

func TestFailureAtEveryBoundaryPromotesNothing(t *testing.T) {
	for _, point := range FaultPoints {
		t.Run(point, func(t *testing.T) { checkFailureAt(t, point) })
	}
}

// checkFailureAt injects one failure at point during a matched capture and
// asserts that no partial set is promoted.
func checkFailureAt(t *testing.T, point string) {
	ctx := context.Background()
	e := newEnv(t)
	e.addArtifact("art-1", testBlob, "hello", true)
	o := e.opts(ModeMatchedStopped)
	hit := false
	boom := errors.New("injected " + point)
	o.Fault = func(p string) error {
		if p == point && !hit {
			hit = true
			return boom
		}
		return nil
	}
	res, err := Create(ctx, o)
	if !hit {
		t.Fatalf("fault point %q was never reached", point)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
	sets := publishedSets(t, e.root)
	if point == "root.dirsync" {
		assertPromotedSetValid(t, sets, res.Path)
		return
	}
	if len(sets) != 0 {
		t.Fatalf("partial set promoted: %v", sets)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.root, StagingDir)); len(entries) != 0 {
		t.Fatalf("staging not cleaned: %v", entries)
	}
}

// assertPromotedSetValid: a failure after the rename leaves a complete set.
func assertPromotedSetValid(t *testing.T, sets []string, path string) {
	t.Helper()
	if len(sets) != 1 {
		t.Fatalf("sets = %v", sets)
	}
	if rep, err := Verify(context.Background(), path); err != nil || !rep.OK {
		t.Fatalf("promoted set is not valid: %v %+v", err, rep)
	}
}

func TestMatchedRefusesUnsettledWritersAndNeverStopsThem(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	// A running controller holds the instance lock.
	controller, err := maintenance.TryLock(maintenance.ControllerLockPath(e.dir))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Create(ctx, e.opts(ModeMatchedStopped))
	if !errors.Is(err, maintenance.ErrUnsettled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := maintenance.TryLock(maintenance.ControllerLockPath(e.dir)); !errors.Is(err, maintenance.ErrHeld) {
		t.Fatal("capture disturbed the controller's lock")
	}
	_ = controller.Release()

	// Live agent container.
	o := e.opts(ModeMatchedStopped)
	o.Settlement = &maintenance.Settlement{LockPath: maintenance.ControllerLockPath(e.dir), Containers: liveLister{}}
	if _, err := Create(ctx, o); !errors.Is(err, maintenance.ErrUnsettled) {
		t.Fatalf("containers: %v", err)
	}
	if got := publishedSets(t, e.root); len(got) != 0 {
		t.Fatalf("sets published while unsettled: %v", got)
	}
	// Database-only capture stays available while the controller runs.
	c2, _ := maintenance.TryLock(maintenance.ControllerLockPath(e.dir))
	defer c2.Release()
	if _, err := Create(ctx, e.opts(ModeDatabaseOnly)); err != nil {
		t.Fatalf("database_only must not need the fence: %v", err)
	}
}

type liveLister struct{}

func (liveLister) LiveOwned(context.Context) ([]string, error) {
	return []string{"abcdef0123456789"}, nil
}

func TestMatchedCapturesArtifactsAndConfigAndRecordsFence(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.addArtifact("art-1", testBlob, "hello", true)
	e.addArtifact("art-2", testBlob2, "world!", true)
	res, err := Create(ctx, e.opts(ModeMatchedStopped))
	if err != nil {
		t.Fatal(err)
	}
	m := res.Manifest
	if m.Fence == nil || !m.Fence.ControllerLock || m.Fence.ContainerEvidence != maintenance.ContainersOperatorAsserted {
		t.Fatalf("fence record %+v", m.Fence)
	}
	roles := map[string]int{}
	for _, f := range m.Files {
		roles[f.Role]++
	}
	if roles[RoleConfig] != 1 || roles[RoleArtifact] != 2 || len(m.Artifacts) != 2 {
		t.Fatalf("files %+v artifacts %d", roles, len(m.Artifacts))
	}
	if rep, err := Verify(ctx, res.Path); err != nil || !rep.OK || rep.Files != 4 {
		t.Fatalf("verify: %v %+v", err, rep)
	}
	// The fence is released after capture: a controller can start again.
	l, err := maintenance.TryLock(maintenance.ControllerLockPath(e.dir))
	if err != nil {
		t.Fatalf("fence leaked: %v", err)
	}
	_ = l.Release()
}

func TestMatchedFailsWhenReferencedBlobIsMissing(t *testing.T) {
	e := newEnv(t)
	e.addArtifact("art-1", testBlob, "hello", false)
	_, err := Create(context.Background(), e.opts(ModeMatchedStopped))
	if !errors.Is(err, ErrArtifactMissing) {
		t.Fatalf("err = %v", err)
	}
	if got := publishedSets(t, e.root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

func TestQuotaAndFreeSpaceFailClosed(t *testing.T) {
	e := newEnv(t)
	o := e.opts(ModeDatabaseOnly)
	o.MaxBytes = 10
	if _, err := Create(context.Background(), o); !errors.Is(err, ErrQuota) {
		t.Fatalf("cap: %v", err)
	}
	o = e.opts(ModeDatabaseOnly)
	o.FreeBytes = func(string) (uint64, error) { return 1, nil }
	if _, err := Create(context.Background(), o); !errors.Is(err, ErrQuota) {
		t.Fatalf("free: %v", err)
	}
	if got := publishedSets(t, e.root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

func TestRootValidation(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	cases := map[string]string{
		"relative":      "backups",
		"inside data":   filepath.Join(e.dir, "backups"),
		"contains data": filepath.Dir(e.dir),
		"inside repo":   filepath.Join(t.TempDir(), "x"), // replaced below
	}
	repo := t.TempDir()
	cases["inside repo"] = filepath.Join(repo, "bk")
	for name, root := range cases {
		o := e.opts(ModeDatabaseOnly)
		o.Root = root
		o.Forbidden = []string{repo}
		if _, err := Create(ctx, o); !errors.Is(err, ErrUnsafeRoot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	// A symlinked root and a group-readable existing root are refused.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(e.root, link); err != nil {
		t.Fatal(err)
	}
	o := e.opts(ModeDatabaseOnly)
	o.Root = link
	if _, err := Create(ctx, o); !errors.Is(err, ErrUnsafeRoot) {
		t.Errorf("symlink: %v", err)
	}
	wide := filepath.Join(t.TempDir(), "wide")
	if err := os.Mkdir(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(wide, 0o755); err != nil {
		t.Fatal(err)
	}
	o.Root = wide
	if _, err := Create(ctx, o); !errors.Is(err, ErrUnsafeRoot) {
		t.Errorf("wide: %v", err)
	}
}

func TestConfigSymlinkIsRejected(t *testing.T) {
	e := newEnv(t)
	link := filepath.Join(e.dir, "link.json")
	if err := os.Symlink(e.cfgFile, link); err != nil {
		t.Fatal(err)
	}
	o := e.opts(ModeMatchedStopped)
	o.ConfigFiles = []string{link}
	if _, err := Create(context.Background(), o); err == nil {
		t.Fatal("symlinked config accepted")
	}
	if got := publishedSets(t, e.root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
}

func TestVerifyRejectsTamperedAndInvalidSets(t *testing.T) {
	ctx := context.Background()
	mk := func(t *testing.T) (*env, string) {
		e := newEnv(t)
		e.addArtifact("art-1", testBlob, "hello", true)
		res, err := Create(ctx, e.opts(ModeMatchedStopped))
		if err != nil {
			t.Fatal(err)
		}
		return e, res.Path
	}
	tests := map[string]func(t *testing.T, e *env, dir string){
		"flipped database byte": func(t *testing.T, _ *env, dir string) {
			p := filepath.Join(dir, DBName)
			b, _ := os.ReadFile(p)
			b[len(b)/2] ^= 0xff
			_ = os.WriteFile(p, b, 0o600)
		},
		"truncated database": func(t *testing.T, _ *env, dir string) {
			p := filepath.Join(dir, DBName)
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, b[:len(b)/2], 0o600)
		},
		"edited manifest": func(t *testing.T, _ *env, dir string) {
			p := filepath.Join(dir, ManifestName)
			b, _ := os.ReadFile(p)
			_ = os.WriteFile(p, []byte(strings.Replace(string(b), `"test"`, `"evil"`, 1)), 0o600)
		},
		"missing marker": func(t *testing.T, _ *env, dir string) { _ = os.Remove(filepath.Join(dir, MarkerName)) },
		"unlisted file": func(t *testing.T, _ *env, dir string) {
			_ = os.WriteFile(filepath.Join(dir, "extra.bin"), []byte("x"), 0o600)
		},
		"config replaced by symlink": func(t *testing.T, e *env, dir string) {
			p := filepath.Join(dir, ConfigDir, "mount-allowlist.json")
			_ = os.Remove(p)
			_ = os.Symlink(e.cfgFile, p)
		},
		"deleted blob": func(t *testing.T, _ *env, dir string) {
			_ = os.Remove(filepath.Join(dir, ArtifactDir, testPrep, testBlob))
		},
		"corrupt blob": func(t *testing.T, _ *env, dir string) {
			_ = os.WriteFile(filepath.Join(dir, ArtifactDir, testPrep, testBlob), []byte("HELLO"), 0o600)
		},
		"manifest hash updated for corrupted database": func(t *testing.T, _ *env, dir string) {
			p := filepath.Join(dir, DBName)
			b, _ := os.ReadFile(p)
			for i := 4096; i < len(b) && i < 4096+512; i++ {
				b[i] ^= 0xff
			}
			_ = os.WriteFile(p, b, 0o600)
			h, n := fileHash(t, p)
			resign(t, dir, func(m *Manifest) { m.Database.SHA256, m.Database.Size = h, n })
		},
		"blob dropped from manifest while database references it": func(t *testing.T, _ *env, dir string) {
			resign(t, dir, func(m *Manifest) {
				var keep []FileEntry
				for _, f := range m.Files {
					if f.Role != RoleArtifact {
						keep = append(keep, f)
					}
				}
				m.Files = keep
			})
			_ = os.Remove(filepath.Join(dir, ArtifactDir, testPrep, testBlob))
		},
		"row counts changed": func(t *testing.T, _ *env, dir string) {
			resign(t, dir, func(m *Manifest) { m.Database.Tables["router_state"]++ })
		},
		"set id mismatch": func(t *testing.T, _ *env, dir string) {
			resign(t, dir, func(m *Manifest) { m.SetID = "20200101T000000Z-db-00000000" })
		},
		"path traversal in manifest": func(t *testing.T, _ *env, dir string) {
			resign(t, dir, func(m *Manifest) {
				m.Files = append(m.Files, FileEntry{Role: RoleConfig, Path: "../../etc/passwd", Size: 1, SHA256: strings.Repeat("0", 64)})
			})
		},
	}
	for name, tamper := range tests {
		t.Run(name, func(t *testing.T) {
			e, dir := mk(t)
			tamper(t, e, dir)
			rep, err := Verify(ctx, dir)
			if err == nil || rep.OK {
				t.Fatalf("tampered set accepted: %+v", rep)
			}
			if !errors.Is(err, ErrInvalidSet) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestVerifyRejectsFutureFormatAndNewerSchema(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	res, err := Create(ctx, e.opts(ModeDatabaseOnly))
	if err != nil {
		t.Fatal(err)
	}
	// Newer schema: add a table to the set's database, then re-seal honestly.
	newer := t.TempDir()
	copyDir(t, res.Path, filepath.Join(newer, res.SetID))
	nd := filepath.Join(newer, res.SetID)
	db := filepath.Join(nd, DBName)
	execSQL(t, db, `CREATE TABLE table_from_the_future(x)`)
	info, err := store.InspectDB(ctx, db)
	if err != nil {
		t.Fatal(err)
	}
	resign(t, nd, func(m *Manifest) {
		m.Database.SHA256, m.Database.Size = info.SHA256, info.Bytes
		m.Database.SchemaFingerprint, m.Database.Tables = info.SchemaFingerprint, info.Tables
	})
	if _, err := Verify(ctx, nd); !errors.Is(err, store.ErrSchemaNewer) {
		t.Fatalf("newer schema: %v", err)
	}
	// Future manifest format.
	resign(t, res.Path, func(m *Manifest) { m.FormatVersion = FormatVersion + 1 })
	if _, err := Verify(ctx, res.Path); !errors.Is(err, ErrUnsupportedFormat) {
		t.Fatalf("future format: %v", err)
	}
}

func TestVerifyDoesNotModifyTheSet(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	e.addArtifact("art-1", testBlob, "hello", true)
	res, _ := Create(ctx, e.opts(ModeMatchedStopped))
	before := dirDigest(t, res.Path)
	if _, err := Verify(ctx, res.Path); err != nil {
		t.Fatal(err)
	}
	if dirDigest(t, res.Path) != before {
		t.Fatal("verify modified the set")
	}
}

func TestListIgnoresStagingAndFlagsUnsealed(t *testing.T) {
	e := newEnv(t)
	res, _ := Create(context.Background(), e.opts(ModeDatabaseOnly))
	_ = os.MkdirAll(filepath.Join(e.root, StagingDir, "junk"), 0o700)
	_ = os.Remove(filepath.Join(res.Path, MarkerName))
	l, _ := List(e.root)
	if len(l) != 1 || l[0].Sealed || l[0].Problem == "" {
		t.Fatalf("list %+v", l)
	}
}
