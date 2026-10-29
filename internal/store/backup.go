package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	sqlite3 "github.com/ncruces/go-sqlite3"
	sqlitedriver "github.com/ncruces/go-sqlite3/driver"
)

// Database snapshots (CP-007). A snapshot is produced by SQLite's online backup
// API from a dedicated connection that is private to the snapshot. It never
// borrows the application's pooled connection, so the single-connection pool in
// Open cannot be starved or deadlocked by a long copy, and WAL readers do not
// block writers. Nothing here applies migrations: the same functions run
// against a stopped store from the operator CLI and against restored copies.

// Errors reported by the snapshot and inspection helpers.
var (
	// ErrSnapshotIntegrity: integrity_check or foreign_key_check failed.
	ErrSnapshotIntegrity = errors.New("store: database integrity check failed")
	// ErrSchemaNewer: the database holds tables or columns this binary does not
	// know, so it was written by a newer binary and must not be opened here.
	ErrSchemaNewer = errors.New("store: database schema is newer than this binary")
	// ErrSnapshotExists: the destination already exists; snapshots never overwrite.
	ErrSnapshotExists = errors.New("store: snapshot destination already exists")
)

// DefaultSnapshotStepPages is the number of pages copied per backup step.
const DefaultSnapshotStepPages = 2048

// SnapshotOptions tunes SnapshotFile. The zero value is correct for online use.
type SnapshotOptions struct {
	// StepPages bounds one backup step (default DefaultSnapshotStepPages).
	StepPages int
	// Fault is an optional test hook called before each durable step with a
	// stable point name; a non-nil error aborts the snapshot there.
	Fault func(point string) error
}

func (o SnapshotOptions) fault(point string) error {
	if o.Fault == nil {
		return nil
	}
	return o.Fault(point)
}

// SnapshotInfo describes a database file as seen by an offline inspection.
type SnapshotInfo struct {
	Bytes               int64            `json:"bytes"`
	SHA256              string           `json:"sha256"`
	SQLiteSchemaVersion int64            `json:"sqlite_schema_version"`
	SchemaFingerprint   string           `json:"schema_fingerprint"`
	IntegrityOK         bool             `json:"integrity_ok"`
	Tables              map[string]int64 `json:"tables"` // row counts of ordinary tables
}

// fileDSN builds a SQLite file URI for an absolute path with the given query.
func fileDSN(path, query string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("store: resolve %q: %w", path, err)
	}
	u := url.URL{Path: abs}
	dsn := "file:" + u.EscapedPath()
	if query != "" {
		dsn += "?" + query
	}
	return dsn, nil
}

// openSnapshotDB opens a private single-connection handle with the sqlite-vec
// enabled driver. It never runs schema statements.
func openSnapshotDB(path, query string) (*sql.DB, error) {
	dsn, err := fileDSN(path, query)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: open %q: %w", filepath.Base(path), err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	return db, nil
}

// Snapshot writes a consistent snapshot of the store to dstPath (see
// SnapshotFile). The application's pooled connection is not used.
func (s *Store) Snapshot(ctx context.Context, dstPath string, opts SnapshotOptions) (SnapshotInfo, error) {
	return SnapshotFile(ctx, s.path, dstPath, opts)
}

// SnapshotFile copies the SQLite database at srcPath, including committed WAL
// content, into a new file at dstPath. dstPath must not exist; it is created
// 0600, converted to a self-contained rollback-journal file (no -wal/-shm
// sidecars), integrity checked, fsynced and described. On any error dstPath is
// removed. Writers may keep committing during the copy: SQLite restarts the
// copy when the source changes, and ctx bounds the total time.
func SnapshotFile(ctx context.Context, srcPath, dstPath string, opts SnapshotOptions) (info SnapshotInfo, err error) {
	if opts.StepPages <= 0 {
		opts.StepPages = DefaultSnapshotStepPages
	}
	if _, serr := os.Stat(srcPath); serr != nil {
		return info, fmt.Errorf("store: snapshot source: %w", serr)
	}
	if err = opts.fault("snapshot.create"); err != nil {
		return info, err
	}
	dst, cerr := os.OpenFile(dstPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if cerr != nil {
		if errors.Is(cerr, os.ErrExist) {
			return info, ErrSnapshotExists
		}
		return info, fmt.Errorf("store: create snapshot file: %w", cerr)
	}
	_ = dst.Close()
	defer func() {
		if err != nil {
			_ = os.Remove(dstPath)
			_ = os.Remove(dstPath + "-journal")
			_ = os.Remove(dstPath + "-wal")
			_ = os.Remove(dstPath + "-shm")
		}
	}()

	if err = copyViaBackupAPI(ctx, srcPath, dstPath, opts); err != nil {
		return info, err
	}
	if err = opts.fault("snapshot.finalize"); err != nil {
		return info, err
	}
	if err = finalizeSnapshot(ctx, dstPath); err != nil {
		return info, err
	}
	if err = opts.fault("snapshot.sync"); err != nil {
		return info, err
	}
	if err = syncFile(dstPath); err != nil {
		return info, err
	}
	if err = os.Chmod(dstPath, 0o600); err != nil {
		return info, fmt.Errorf("store: chmod snapshot: %w", err)
	}
	return InspectDB(ctx, dstPath)
}

func copyViaBackupAPI(ctx context.Context, srcPath, dstPath string, opts SnapshotOptions) error {
	// The source is opened read-write (never created) because a read-only WAL
	// reader cannot run WAL-index recovery and fails with READONLY_RECOVERY
	// when a writer restarts the log mid-copy. The snapshot never writes data
	// to it; at most it takes the usual WAL reader/checkpoint locks.
	src, err := openSnapshotDB(srcPath, "mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer src.Close()
	conn, err := src.Conn(ctx)
	if err != nil {
		return fmt.Errorf("store: snapshot source connection: %w", err)
	}
	defer conn.Close()
	dstURI, err := fileDSN(dstPath, "")
	if err != nil {
		return err
	}
	return conn.Raw(func(driverConn any) error {
		dc, ok := driverConn.(sqlitedriver.Conn)
		if !ok {
			return errors.New("store: unexpected sqlite driver connection type")
		}
		raw := dc.Raw()
		b, err := raw.BackupInit("main", dstURI)
		if err != nil {
			return fmt.Errorf("store: backup init: %w", err)
		}
		defer b.Close()
		for {
			if cerr := ctx.Err(); cerr != nil {
				return fmt.Errorf("store: snapshot cancelled: %w", cerr)
			}
			done, serr := b.Step(opts.StepPages)
			if serr != nil {
				if isBusyOrLocked(serr) {
					// A writer holds the source; retry until ctx expires.
					select {
					case <-ctx.Done():
					case <-time.After(10 * time.Millisecond):
					}
					continue
				}
				return fmt.Errorf("store: backup step: %w", serr)
			}
			if done {
				break
			}
		}
		if err := opts.fault("snapshot.copy_done"); err != nil {
			return err
		}
		if err := b.Close(); err != nil {
			return fmt.Errorf("store: backup finish: %w", err)
		}
		return nil
	})
}

func isBusyOrLocked(err error) bool {
	var se *sqlite3.Error
	if errors.As(err, &se) {
		c := se.Code()
		return c == sqlite3.BUSY || c == sqlite3.LOCKED
	}
	return false
}

// finalizeSnapshot converts the copy to journal_mode=DELETE so it is a single
// self-contained file, and runs the integrity checks before anything is kept.
func finalizeSnapshot(ctx context.Context, dstPath string) error {
	db, err := openSnapshotDB(dstPath, "mode=rw&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	var mode string
	if err := db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return fmt.Errorf("store: snapshot journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("store: snapshot journal mode is %q", mode)
	}
	return checkIntegrity(ctx, db)
}

func checkIntegrity(ctx context.Context, db *sql.DB) error {
	rows, err := db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("store: integrity_check: %w", err)
	}
	defer rows.Close()
	var problems []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return fmt.Errorf("store: integrity_check: %w", err)
		}
		if line != "ok" {
			problems = append(problems, line)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: integrity_check: %w", err)
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrSnapshotIntegrity, firstN(problems, 3))
	}
	fk, err := db.QueryContext(ctx, "PRAGMA foreign_key_check")
	if err != nil {
		return fmt.Errorf("store: foreign_key_check: %w", err)
	}
	defer fk.Close()
	if fk.Next() {
		return fmt.Errorf("%w: foreign key violation", ErrSnapshotIntegrity)
	}
	return fk.Err()
}

func firstN(s []string, n int) string {
	if len(s) > n {
		s = s[:n]
	}
	return strings.Join(s, "; ")
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("store: open for sync: %w", err)
	}
	defer f.Close()
	if err := f.Sync(); err != nil {
		return fmt.Errorf("store: fsync: %w", err)
	}
	return nil
}

// InspectDB opens the database read-only without running migrations and
// reports integrity, schema fingerprint, per-table row counts and the file
// hash. It fails with ErrSnapshotIntegrity when the checks do not pass.
func InspectDB(ctx context.Context, path string) (SnapshotInfo, error) {
	var info SnapshotInfo
	fi, err := os.Stat(path)
	if err != nil {
		return info, fmt.Errorf("store: inspect: %w", err)
	}
	info.Bytes = fi.Size()
	if info.SHA256, err = fileSHA256(path); err != nil {
		return info, err
	}
	db, err := openSnapshotDB(path, "mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return info, err
	}
	defer db.Close()
	if err := checkIntegrity(ctx, db); err != nil {
		return info, err
	}
	info.IntegrityOK = true
	if err := db.QueryRowContext(ctx, "PRAGMA schema_version").Scan(&info.SQLiteSchemaVersion); err != nil {
		return info, fmt.Errorf("store: schema_version: %w", err)
	}
	schema, err := readSchema(ctx, db)
	if err != nil {
		return info, err
	}
	info.SchemaFingerprint = schema.fingerprint()
	info.Tables = make(map[string]int64, len(schema.cols))
	for _, t := range schema.tables() {
		if schema.virtual[t] {
			continue
		}
		var n int64
		if err := db.QueryRowContext(ctx, `SELECT count(*) FROM "`+strings.ReplaceAll(t, `"`, `""`)+`"`).Scan(&n); err != nil {
			return info, fmt.Errorf("store: count %s: %w", t, err)
		}
		info.Tables[t] = n
	}
	return info, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("store: hash: %w", err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", fmt.Errorf("store: hash: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// dbSchema is the table and column layout of a database.
type dbSchema struct {
	cols    map[string][]string // table -> ordered column names
	virtual map[string]bool
}

func (d dbSchema) tables() []string {
	out := make([]string, 0, len(d.cols))
	for t := range d.cols {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

func (d dbSchema) fingerprint() string {
	h := sha256.New()
	for _, t := range d.tables() {
		fmt.Fprintf(h, "%s(%s)\n", t, strings.Join(d.cols[t], ","))
	}
	return hex.EncodeToString(h.Sum(nil))
}

func readSchema(ctx context.Context, db *sql.DB) (dbSchema, error) {
	d := dbSchema{cols: map[string][]string{}, virtual: map[string]bool{}}
	rows, err := db.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite\_%' ESCAPE '\'`)
	if err != nil {
		return d, fmt.Errorf("store: read schema: %w", err)
	}
	var names []string
	for rows.Next() {
		var name string
		var ddl sql.NullString
		if err := rows.Scan(&name, &ddl); err != nil {
			rows.Close()
			return d, fmt.Errorf("store: read schema: %w", err)
		}
		names = append(names, name)
		d.virtual[name] = strings.HasPrefix(strings.ToUpper(strings.TrimSpace(ddl.String)), "CREATE VIRTUAL")
	}
	if err := rows.Close(); err != nil {
		return d, err
	}
	for _, n := range names {
		cr, err := db.QueryContext(ctx, `SELECT name FROM pragma_table_xinfo(?) ORDER BY cid`, n)
		if err != nil {
			return d, fmt.Errorf("store: read columns of %s: %w", n, err)
		}
		cols := []string{}
		for cr.Next() {
			var c string
			if err := cr.Scan(&c); err != nil {
				cr.Close()
				return d, err
			}
			cols = append(cols, c)
		}
		if err := cr.Close(); err != nil {
			return d, err
		}
		d.cols[n] = cols
	}
	return d, nil
}

// referenceSchema is what this binary's Open would create, computed on a
// scratch database so no stored ledger is needed. SQLite's schema_version is a
// change counter, not application compatibility evidence.
func referenceSchema(ctx context.Context) (dbSchema, error) {
	dir, err := os.MkdirTemp("", "gopod-schema-ref-")
	if err != nil {
		return dbSchema{}, fmt.Errorf("store: schema reference dir: %w", err)
	}
	defer os.RemoveAll(dir)
	ref := filepath.Join(dir, "ref.sqlite")
	st, err := Open(ctx, ref, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		return dbSchema{}, fmt.Errorf("store: build schema reference: %w", err)
	}
	if err := st.Close(); err != nil {
		return dbSchema{}, err
	}
	db, err := openSnapshotDB(ref, "mode=ro")
	if err != nil {
		return dbSchema{}, err
	}
	defer db.Close()
	return readSchema(ctx, db)
}

// CheckSchemaCompat reports ErrSchemaNewer when the database at path contains a
// table or column that this binary's schema does not define. An older database
// (missing newer tables or columns) is compatible: Open migrates it forward.
func CheckSchemaCompat(ctx context.Context, path string) error {
	db, err := openSnapshotDB(path, "mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return err
	}
	defer db.Close()
	have, err := readSchema(ctx, db)
	if err != nil {
		return err
	}
	want, err := referenceSchema(ctx)
	if err != nil {
		return err
	}
	var unknown []string
	for _, t := range have.tables() {
		wc, ok := want.cols[t]
		if !ok {
			unknown = append(unknown, "table "+t)
			continue
		}
		known := map[string]bool{}
		for _, c := range wc {
			known[c] = true
		}
		for _, c := range have.cols[t] {
			if !known[c] {
				unknown = append(unknown, "column "+t+"."+c)
			}
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("%w: %s", ErrSchemaNewer, firstN(unknown, 5))
	}
	return nil
}
