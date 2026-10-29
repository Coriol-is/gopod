package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestRootAliasOfForbiddenTreeIsRefusedByIdentity(t *testing.T) {
	e := newEnv(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(e.dir, alias); err != nil {
		t.Fatal(err)
	}
	for name, root := range map[string]string{
		"alias then subdir": filepath.Join(alias, "bk"),
		"alias parent":      filepath.Dir(alias), // does not contain data; must stay allowed below
	} {
		o := e.opts(ModeDatabaseOnly)
		o.Root = root
		_, err := Create(context.Background(), o)
		if name == "alias then subdir" && !errors.Is(err, ErrUnsafeRoot) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if !sameTree(filepath.Join(alias, "x"), e.dir) {
		t.Error("alias path not recognised as inside the data dir by identity")
	}
	if sameTree(filepath.Join(filepath.Dir(e.dir), "bk-not-yet"), e.dir) {
		t.Error("sibling that does not exist yet was flagged")
	}
}

func TestCleanStagingRemovesOnlyStaleUnlockedEntries(t *testing.T) {
	e := newEnv(t)
	staging := filepath.Join(e.root, StagingDir)
	mk := func(name string, age time.Duration, lock bool) (string, *maintenance.Lock) {
		d := filepath.Join(staging, name)
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
		_ = os.WriteFile(filepath.Join(d, "junk"), make([]byte, 1000), 0o600)
		old := time.Now().Add(-age)
		_ = os.Chtimes(d, old, old)
		var l *maintenance.Lock
		if lock {
			var err error
			if l, err = maintenance.TryLock(d + stageLockSuffix); err != nil {
				t.Fatal(err)
			}
		}
		return d, l
	}
	stale, _ := mk("stale", 2*time.Hour, false)
	live, l := mk("live-but-old", 2*time.Hour, true)
	defer l.Release()
	fresh, _ := mk("fresh", time.Minute, false)
	// Create runs the cleanup first.
	if _, err := Create(context.Background(), e.opts(ModeDatabaseOnly)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); err == nil {
		t.Error("stale unlocked staging was kept")
	}
	if _, err := os.Stat(live); err != nil {
		t.Error("a live capture's staging directory was removed")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("fresh staging was removed")
	}
	n, err := usedBytes(e.root)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("published set not counted")
	}
	before := n
	_ = os.WriteFile(filepath.Join(fresh, "big"), make([]byte, 1<<20), 0o600)
	if after, _ := usedBytes(e.root); after != before {
		t.Fatalf("staging counted toward usage: %d vs %d", after, before)
	}
}

func TestForeignKeyViolationsWarnInDatabaseOnlyAndRefuseMatched(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	conn, err := e.st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO artifacts(artifact_id,namespace_id,namespace_epoch,state,relative_blob_key,created_at,preparation_id) VALUES('o1','ns',1,'preparing','kk',1,'nope')`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatal(err)
		}
	}
	conn.Close()

	if _, err := Create(ctx, e.opts(ModeMatchedStopped)); !errors.Is(err, store.ErrForeignKeyViolation) {
		t.Fatalf("matched_stopped must stay strict: %v", err)
	}
	if got := publishedSets(t, e.root); len(got) != 0 {
		t.Fatalf("published %v", got)
	}
	res, err := Create(ctx, e.opts(ModeDatabaseOnly))
	if err != nil {
		t.Fatalf("database_only: %v", err)
	}
	if len(res.Manifest.Warnings) != 1 || !strings.Contains(res.Manifest.Warnings[0], "artifacts=1") {
		t.Fatalf("warnings %v", res.Manifest.Warnings)
	}
	rep, err := Verify(ctx, res.Path)
	if err != nil || !rep.OK || !strings.Contains(strings.Join(rep.Warnings, "|"), "artifacts=1") {
		t.Fatalf("verify: %v %+v", err, rep)
	}
	// Hiding the violation from the manifest is a mismatch, not a pass.
	resign(t, res.Path, func(m *Manifest) { m.Database.ForeignKeyViolations = nil })
	if _, err := Verify(ctx, res.Path); err == nil {
		t.Fatal("manifest that hides violations accepted")
	}
}

func TestVerifyRejectsSymlinkedParentDirectories(t *testing.T) {
	ctx := context.Background()
	for _, sub := range []string{ConfigDir, ArtifactDir} {
		t.Run(sub, func(t *testing.T) {
			e := newEnv(t)
			e.addArtifact("art-1", testBlob, "hello", true)
			res, err := Create(ctx, e.opts(ModeMatchedStopped))
			if err != nil {
				t.Fatal(err)
			}
			real := filepath.Join(t.TempDir(), "elsewhere")
			copyDir(t, filepath.Join(res.Path, sub), real)
			if err := os.RemoveAll(filepath.Join(res.Path, sub)); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(real, filepath.Join(res.Path, sub)); err != nil {
				t.Fatal(err)
			}
			rep, err := Verify(ctx, res.Path)
			if err == nil || rep.OK {
				t.Fatalf("symlinked %s/ accepted: %+v", sub, rep)
			}
		})
	}
}

func TestVerifyStatesHashesAreNotAuthenticity(t *testing.T) {
	e := newEnv(t)
	res, _ := Create(context.Background(), e.opts(ModeDatabaseOnly))
	rep, err := Verify(context.Background(), res.Path)
	if err != nil || !strings.Contains(rep.Notice, "not tampering or authenticity") {
		t.Fatalf("notice %q %v", rep.Notice, err)
	}
}

func TestVerifyScratchDefaultsUnderRootAndIsConfigurable(t *testing.T) {
	ctx := context.Background()
	e := newEnv(t)
	res, err := Create(ctx, e.opts(ModeDatabaseOnly))
	if err != nil {
		t.Fatal(err)
	}
	// The system temp dir is unusable: the default must not need it.
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "does-not-exist"))
	if rep, err := Verify(ctx, res.Path); err != nil || !rep.OK {
		t.Fatalf("default scratch: %v %+v", err, rep)
	}
	custom := filepath.Join(t.TempDir(), "scratch")
	if rep, err := VerifyWith(ctx, res.Path, VerifyOptions{ScratchDir: custom}); err != nil || !rep.OK {
		t.Fatalf("custom scratch: %v %+v", err, rep)
	}
	if entries, _ := os.ReadDir(custom); len(entries) != 0 {
		t.Fatalf("scratch not cleaned: %v", entries)
	}
	if _, err := os.Stat(custom); err != nil {
		t.Fatal("custom scratch dir was not used")
	}
}
