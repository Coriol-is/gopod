package store

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
)

func seedState(t *testing.T, st *Store, n int, prefix string) {
	t.Helper()
	for i := 0; i < n; i++ {
		if err := st.SetState(context.Background(), fmt.Sprintf("%s%d", prefix, i), "v"); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
}

func stateCount(t *testing.T, st *Store) int64 {
	t.Helper()
	var n int64
	if err := st.DB().QueryRow(`SELECT count(*) FROM router_state`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestSnapshotIncludesWALContentAndIsSelfContained(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	seedState(t, st, 50, "k")
	if fi, err := os.Stat(st.Path() + "-wal"); err != nil || fi.Size() == 0 {
		t.Fatalf("test premise: expected unflushed WAL content, got %v %v", fi, err)
	}
	dst := filepath.Join(dir, "snap.sqlite")
	info, err := st.Snapshot(ctx, dst, SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if !info.IntegrityOK || info.Tables["router_state"] != 50 || info.SchemaFingerprint == "" || len(info.SHA256) != 64 {
		t.Fatalf("info: %+v", info)
	}
	if fi, _ := os.Stat(dst); fi.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", fi.Mode())
	}
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if _, err := os.Stat(dst + side); err == nil {
			t.Fatalf("snapshot has sidecar %s", side)
		}
	}
	// The real Store opens the snapshot and sees every row.
	rs := openStoreAt(t, dst)
	if got := stateCount(t, rs); got != 50 {
		t.Fatalf("restored rows = %d", got)
	}
}

func TestSnapshotWithConcurrentWriterIsConsistent(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	seedState(t, st, 20, "seed")
	var stop atomic.Bool
	var wg sync.WaitGroup
	var written atomic.Int64
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; !stop.Load(); i++ {
			if err := st.SetState(ctx, fmt.Sprintf("w%d", i), "x"); err != nil {
				return
			}
			written.Add(1)
		}
	}()
	for written.Load() < 5 {
		time.Sleep(time.Millisecond)
	}
	var infos []SnapshotInfo
	for i := 0; i < 3; i++ {
		c, cancel := context.WithTimeout(ctx, 30*time.Second)
		info, err := st.Snapshot(c, filepath.Join(dir, fmt.Sprintf("s%d.sqlite", i)), SnapshotOptions{StepPages: 4})
		cancel()
		if err != nil {
			t.Fatalf("snapshot %d: %v", i, err)
		}
		infos = append(infos, info)
	}
	stop.Store(true)
	wg.Wait()
	for i, info := range infos {
		if !info.IntegrityOK || info.Tables["router_state"] < 20 || info.Tables["router_state"] > stateCount(t, st) {
			t.Fatalf("snapshot %d inconsistent: %+v", i, info)
		}
	}
}

func TestSnapshotDoesNotNeedTheApplicationConnection(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	seedState(t, st, 5, "k")
	// Hold the only pooled connection inside an open write transaction.
	tx, err := st.DB().BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO router_state(key,value) VALUES('uncommitted','x')`); err != nil {
		t.Fatal(err)
	}
	c, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	info, err := st.Snapshot(c, filepath.Join(dir, "snap.sqlite"), SnapshotOptions{})
	if err != nil {
		t.Fatalf("snapshot while app connection busy: %v", err)
	}
	if info.Tables["router_state"] != 5 {
		t.Fatalf("snapshot saw uncommitted data: %+v", info.Tables)
	}
	_ = tx.Rollback()
}

func TestSnapshotRefusesExistingDestinationAndCleansUp(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	dst := filepath.Join(dir, "snap.sqlite")
	if err := os.WriteFile(dst, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Snapshot(ctx, dst, SnapshotOptions{}); !errors.Is(err, ErrSnapshotExists) {
		t.Fatalf("err = %v", err)
	}
	if b, _ := os.ReadFile(dst); string(b) != "keep" {
		t.Fatal("existing destination was modified")
	}
	cctx, cancel := context.WithCancel(ctx)
	cancel()
	dst2 := filepath.Join(dir, "snap2.sqlite")
	if _, err := st.Snapshot(cctx, dst2, SnapshotOptions{}); err == nil {
		t.Fatal("cancelled snapshot succeeded")
	}
	if _, err := os.Stat(dst2); err == nil {
		t.Fatal("cancelled snapshot left a file")
	}
}

func TestSnapshotFaultPointsLeaveNoFile(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	for _, point := range []string{"snapshot.create", "snapshot.copy_done", "snapshot.finalize", "snapshot.sync"} {
		dst := filepath.Join(dir, point+".sqlite")
		boom := errors.New("injected " + point)
		_, err := st.Snapshot(ctx, dst, SnapshotOptions{Fault: func(p string) error {
			if p == point {
				return boom
			}
			return nil
		}})
		if !errors.Is(err, boom) {
			t.Fatalf("%s: err = %v", point, err)
		}
		if _, err := os.Stat(dst); err == nil {
			t.Fatalf("%s: partial snapshot left behind", point)
		}
	}
}

func TestInspectDBRejectsCorruptAndTruncated(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	seedState(t, st, 200, "k")
	good := filepath.Join(dir, "good.sqlite")
	if _, err := st.Snapshot(ctx, good, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(good)

	trunc := filepath.Join(dir, "trunc.sqlite")
	if err := os.WriteFile(trunc, raw[:len(raw)/2], 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDB(ctx, trunc); err == nil {
		t.Fatal("truncated database accepted")
	}
	garbage := filepath.Join(dir, "garbage.sqlite")
	if err := os.WriteFile(garbage, []byte("this is not a sqlite database at all, just text........."), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := InspectDB(ctx, garbage); err == nil {
		t.Fatal("garbage accepted")
	}
}

func TestCheckSchemaCompat(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	snap := filepath.Join(dir, "snap.sqlite")
	if _, err := st.Snapshot(ctx, snap, SnapshotOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := CheckSchemaCompat(ctx, snap); err != nil {
		t.Fatalf("current schema rejected: %v", err)
	}

	newerTable := filepath.Join(dir, "newer-table.sqlite")
	copyFile(t, snap, newerTable)
	execOn(t, newerTable, `CREATE TABLE from_the_future(id INTEGER)`)
	if err := CheckSchemaCompat(ctx, newerTable); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("newer table: %v", err)
	}
	newerCol := filepath.Join(dir, "newer-col.sqlite")
	copyFile(t, snap, newerCol)
	execOn(t, newerCol, `ALTER TABLE router_state ADD COLUMN future_col TEXT`)
	if err := CheckSchemaCompat(ctx, newerCol); !errors.Is(err, ErrSchemaNewer) {
		t.Fatalf("newer column: %v", err)
	}
	older := filepath.Join(dir, "older.sqlite")
	copyFile(t, snap, older)
	execOn(t, older, `DROP TABLE task_attempts`)
	if err := CheckSchemaCompat(ctx, older); err != nil {
		t.Fatalf("older schema must be compatible: %v", err)
	}
}

func TestReadWorkEvidenceAndArtifactInventoryOnOldSchema(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	old := filepath.Join(dir, "old.sqlite")
	execOn(t, old, `CREATE TABLE turns(id INTEGER PRIMARY KEY, status TEXT)`)
	execOn(t, old, `INSERT INTO turns(status) VALUES('running'),('done'),('pending')`)
	ev, err := ReadWorkEvidence(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if ev.TurnsRunning != 1 || ev.TurnsPending != 1 || !ev.Unsettled() {
		t.Fatalf("evidence %+v", ev)
	}
	inv, err := ArtifactInventory(ctx, old)
	if err != nil || len(inv) != 0 {
		t.Fatalf("inventory %v %v", inv, err)
	}
}

func openStoreAt(t *testing.T, path string) *Store {
	t.Helper()
	st, err := Open(context.Background(), path, nil)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func copyFile(t *testing.T, from, to string) {
	t.Helper()
	b, err := os.ReadFile(from)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

func execOn(t *testing.T, path, stmt string) {
	t.Helper()
	db, err := openSnapshotDB(path, "mode=rwc")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(stmt); err != nil {
		t.Fatalf("exec %q: %v", stmt, err)
	}
}

// orphan inserts an artifact whose reservation does not exist, with foreign
// keys briefly off, to model a pre-existing violation.
func orphan(t *testing.T, st *Store) {
	t.Helper()
	ctx := context.Background()
	conn, err := st.DB().Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	for _, q := range []string{
		`PRAGMA foreign_keys=OFF`,
		`INSERT INTO artifacts(artifact_id,namespace_id,namespace_epoch,state,relative_blob_key,created_at,preparation_id) VALUES('a1','ns',1,'preparing','k1',1,'missing-prep')`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := conn.ExecContext(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
}

func TestSnapshotForeignKeyViolationsNamedAndOptional(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	orphan(t, st)
	dst := filepath.Join(dir, "strict.sqlite")
	_, err := st.Snapshot(ctx, dst, SnapshotOptions{})
	if !errors.Is(err, ErrForeignKeyViolation) || !errors.Is(err, ErrSnapshotIntegrity) {
		t.Fatalf("err = %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "artifacts=1") || strings.Contains(msg, "missing-prep") {
		t.Fatalf("message must name table and count only: %q", msg)
	}
	if _, serr := os.Stat(dst); serr == nil {
		t.Fatal("strict snapshot left a file")
	}
	info, err := st.Snapshot(ctx, filepath.Join(dir, "lenient.sqlite"), SnapshotOptions{AllowForeignKeyViolations: true})
	if err != nil || info.ForeignKeyViolations["artifacts"] != 1 || !info.IntegrityOK {
		t.Fatalf("lenient: %v %+v", err, info.ForeignKeyViolations)
	}
}

func TestSnapshotTimeoutMessageExplainsRestarts(t *testing.T) {
	dir := t.TempDir()
	st := openStoreAt(t, filepath.Join(dir, "store.sqlite"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	_, err := st.Snapshot(ctx, filepath.Join(dir, "s.sqlite"), SnapshotOptions{Fault: func(p string) error {
		if p == "snapshot.step" {
			cancel()
		}
		return nil
	}})
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "concurrent writer") || !strings.Contains(err.Error(), "restarts") {
		t.Fatalf("err = %v", err)
	}
}
