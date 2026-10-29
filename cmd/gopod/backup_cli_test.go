package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/Coriol-is/gopod/internal/config"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

func cliEnv(t *testing.T) (dataDir, backups string) {
	t.Helper()
	base := t.TempDir()
	dataDir, backups = filepath.Join(base, "data"), filepath.Join(base, "bk")
	t.Setenv("GOPOD_DATA_DIR", dataDir)
	t.Setenv("GOPOD_BACKUP_DIR", backups)
	t.Setenv("GOPOD_REPO_ROOT", filepath.Join(base, "repo"))
	st, err := store.Open(context.Background(), filepath.Join(dataDir, "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetState(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return dataDir, backups
}

func runCLI(t *testing.T, args ...string) (int, string) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runBackupCLI(args, &out, &errb)
	return code, out.String() + errb.String()
}

func TestBackupCLICreateVerifyListWithoutSqlite3(t *testing.T) {
	_, backups := cliEnv(t)
	t.Setenv("PATH", "") // proves no external sqlite3 (or any binary) is needed
	code, out := runCLI(t, "create")
	if code != 0 {
		t.Fatalf("create: %d %s", code, out)
	}
	sets, _ := os.ReadDir(backups)
	var id string
	for _, e := range sets {
		if !strings.HasPrefix(e.Name(), ".") {
			id = e.Name()
		}
	}
	if id == "" {
		t.Fatalf("no set in %s: %s", backups, out)
	}
	if code, out = runCLI(t, "verify", id); code != 0 || !strings.Contains(out, "OK") {
		t.Fatalf("verify: %d %s", code, out)
	}
	if code, out = runCLI(t, "list", "--json"); code != 0 || !strings.Contains(out, id) {
		t.Fatalf("list: %d %s", code, out)
	}
	if code, out = runCLI(t, "nonsense"); code == 0 {
		t.Fatalf("unknown subcommand succeeded: %s", out)
	}
	if code, _ = runCLI(t); code != 2 {
		t.Fatalf("no args: %d", code)
	}
}

func TestMatchedCLIRefusesWhileControllerRuns(t *testing.T) {
	dataDir, backups := cliEnv(t)
	l, err := maintenance.TryLock(maintenance.ControllerLockPath(dataDir))
	if err != nil {
		t.Fatal(err)
	}
	defer l.Release()
	code, out := runCLI(t, "create", "--mode", "matched_stopped", "--assert-no-containers")
	if code != 3 || !strings.Contains(out, "nothing was stopped") {
		t.Fatalf("code %d: %s", code, out)
	}
	if entries, _ := os.ReadDir(backups); len(entries) != 0 {
		t.Fatalf("root not empty: %v", entries)
	}
}

func TestQuarantineStartsNothingAndLeavesStoreUntouched(t *testing.T) {
	dataDir, _ := cliEnv(t)
	t.Setenv("TELEGRAM_BOT_TOKEN", "123:synthetic-not-real")
	t.Setenv("GOPOD_RECOVERY_QUARANTINE", "1")
	cfg, err := config.Load(false)
	if err != nil {
		t.Fatal(err)
	}
	if q, _ := recoveryQuarantine(cfg); !q {
		t.Fatal("quarantine env not honoured")
	}
	// An older-schema store: Store.Open would recreate the dropped table, so an
	// unchanged tree proves no migration ran.
	old, err := store.Open(context.Background(), cfg.StorePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := old.DB().Exec(`DROP TABLE task_verifications`); err != nil {
		t.Fatal(err)
	}
	if _, err := old.DB().Exec(`DROP TABLE task_attempts`); err != nil {
		t.Fatal(err)
	}
	_ = old.Close()
	before := treeDigest(t, dataDir)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runQuarantine(ctx, cfg, "test", testLogger(t)) }()
	time.Sleep(300 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if after := treeDigest(t, dataDir); after != before {
		t.Fatalf("quarantined start modified the data dir:\nbefore %s\nafter  %s", before, after)
	}
}

func TestLatchForcesQuarantineWithoutEnv(t *testing.T) {
	dataDir, _ := cliEnv(t)
	cfg, _ := config.Load(false)
	if q, _ := recoveryQuarantine(cfg); q {
		t.Fatal("quarantine on by default")
	}
	if err := maintenance.SetLatch(dataDir, "20261029T000000Z-db-00000000", "restored", time.Now()); err != nil {
		t.Fatal(err)
	}
	if q, why := recoveryQuarantine(cfg); !q || !strings.Contains(why, "latch") {
		t.Fatalf("latch ignored: %v %q", q, why)
	}
}

func treeDigest(t *testing.T, dir string) string {
	t.Helper()
	var sb strings.Builder
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(p, "-shm") {
			return nil // -shm holds reader marks, not data
		}
		b, _ := os.ReadFile(p)
		sum := sha256.Sum256(b)
		sb.WriteString(p + " " + hex.EncodeToString(sum[:]) + "\n")
		return nil
	})
	return sb.String()
}

func testLogger(t *testing.T) *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError}))
}

func TestLiveOwnedRefusesLiveComposeControllerButNotOneOffRun(t *testing.T) {
	agents := []container.Summary{{ID: "agent-running", State: "running"}, {ID: "agent-exited", State: "exited"}}
	services := []container.Summary{
		{ID: "old-controller", State: "running", Labels: map[string]string{}},
		{ID: "this-capture", State: "running", Labels: map[string]string{composeOneOffLabel: "True"}},
		{ID: "stopped-controller", State: "exited"},
	}
	got := liveOwned(agents, services)
	want := []string{"agent-running", "old-controller"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("got %v want %v", got, want)
	}
	if len(liveOwned(nil, nil)) != 0 {
		t.Fatal("empty inventory must be empty")
	}
}
