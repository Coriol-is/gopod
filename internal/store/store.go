// Package store is the single SQLite-backed persistence layer for gopod.
//
// Per the architectural rules in CLAUDE.md, all DB access must go through
// this package — no other package opens database/sql connections directly.
//
// The driver is github.com/ncruces/go-sqlite3 (pure-Go via WASM, no CGO),
// chosen specifically because the asg017/sqlite-vec WASM extension can be
// auto-loaded into every connection. See ADR D004.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	// The asg017 ncruces binding populates sqlite3.Binary in its init()
	// with a SQLite WASM build that has sqlite-vec compiled in. It must
	// be imported INSTEAD of github.com/ncruces/go-sqlite3/embed (the two
	// would race to set the same Binary variable). This pins us to
	// ncruces/go-sqlite3 v0.32.x — see go.mod.
	_ "github.com/asg017/sqlite-vec-go-bindings/ncruces"
	_ "github.com/ncruces/go-sqlite3/driver" // register "sqlite3" driver
)

// Store is the gopod persistence handle. It wraps a *sql.DB and is
// safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
	log  *slog.Logger
}

// Open opens (and creates if necessary) the SQLite database at path,
// applies the schema (idempotent), and returns a ready Store.
//
// The parent directory is created with 0755 if it does not exist.
func Open(ctx context.Context, path string, log *slog.Logger) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty path")
	}
	if log == nil {
		log = slog.Default()
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("store: create parent dir: %w", err)
	}

	// file: URL with mode=rwc + busy_timeout to play nicely with
	// the auto-checkpoint behavior of WAL mode.
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)&_pragma=synchronous(NORMAL)"

	db, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return nil, fmt.Errorf("store: sql.Open: %w", err)
	}

	// SQLite handles serialization internally; one writer is fine.
	// Allowing many idle connections risks lock contention with WAL.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: ping: %w", err)
	}

	s := &Store{db: db, path: path, log: log}

	if err := s.applySchema(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: apply schema: %w", err)
	}

	if err := s.verifyVecExtension(ctx); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("store: verify sqlite-vec: %w", err)
	}

	log.Info("store opened",
		slog.String("path", path),
		slog.String("vec_version", s.vecVersion(ctx)),
	)
	return s, nil
}

// Close closes the underlying database. Safe to call multiple times.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// DB returns the underlying *sql.DB. Use sparingly — prefer adding methods
// to this package over leaking the handle.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the on-disk path of the database file.
func (s *Store) Path() string { return s.path }

// verifyVecExtension confirms that sqlite-vec is loaded by querying its
// version function. If sqlite_vec.Auto() failed silently, this will
// return an error.
func (s *Store) verifyVecExtension(ctx context.Context) error {
	var v string
	if err := s.db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&v); err != nil {
		return fmt.Errorf("vec_version(): %w", err)
	}
	if v == "" {
		return errors.New("vec_version() returned empty string")
	}
	return nil
}

// vecVersion returns the sqlite-vec version string, or "unknown" if the
// query fails. Safe to call after Open succeeds.
func (s *Store) vecVersion(ctx context.Context) string {
	var v string
	if err := s.db.QueryRowContext(ctx, "SELECT vec_version()").Scan(&v); err != nil {
		return "unknown"
	}
	return v
}
