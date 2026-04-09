package store

import (
	"context"
	"io"
	"log/slog"
	"math"
	"path/filepath"
	"testing"
)

// silentLogger discards everything; keeps test output clean.
func silentLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestOpenAppliesSchemaAndLoadsVec(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.sqlite")

	ctx := context.Background()
	s, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Verify sqlite-vec loaded.
	var vecVer string
	if err := s.DB().QueryRowContext(ctx, "SELECT vec_version()").Scan(&vecVer); err != nil {
		t.Fatalf("vec_version: %v", err)
	}
	if vecVer == "" {
		t.Fatal("vec_version returned empty")
	}

	// Verify the relational tables exist.
	for _, table := range []string{
		"chats", "messages", "registered_chats", "sessions",
		"scheduled_tasks", "task_run_logs", "router_state", "memories",
	} {
		var name string
		err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("missing table %q: %v", table, err)
		}
	}

	// Verify the virtual tables exist.
	for _, vtab := range []string{"memory_vec", "memory_fts"} {
		var name string
		err := s.DB().QueryRowContext(ctx,
			`SELECT name FROM sqlite_master WHERE name=?`, vtab).Scan(&name)
		if err != nil {
			t.Errorf("missing virtual table %q: %v", vtab, err)
		}
	}
}

func TestOpenIsIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.sqlite")
	ctx := context.Background()

	s1, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("first Open: %v", err)
	}
	if err := s1.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}

	s2, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("second Open (re-applying schema): %v", err)
	}
	t.Cleanup(func() { _ = s2.Close() })
}

func TestVecVirtualTableUsable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "store.sqlite")
	ctx := context.Background()

	s, err := Open(ctx, path, silentLogger())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })

	// Insert two vectors and run a KNN query against them.
	// 1024-dim vectors keep this consistent with the schema in MEMORY.md.
	v1 := make([]float32, 1024)
	v2 := make([]float32, 1024)
	for i := range v1 {
		v1[i] = 1
		v2[i] = -1
	}

	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO memory_vec(rowid, embedding) VALUES (?, ?)`,
		1, float32sToBlob(v1),
	); err != nil {
		t.Fatalf("insert v1: %v", err)
	}
	if _, err := s.DB().ExecContext(ctx,
		`INSERT INTO memory_vec(rowid, embedding) VALUES (?, ?)`,
		2, float32sToBlob(v2),
	); err != nil {
		t.Fatalf("insert v2: %v", err)
	}

	rows, err := s.DB().QueryContext(ctx, `
	  SELECT rowid, distance
	    FROM memory_vec
	   WHERE embedding MATCH ?
	     AND k = 2
	   ORDER BY distance
	`, float32sToBlob(v1))
	if err != nil {
		t.Fatalf("knn query: %v", err)
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		var id int64
		var dist float64
		if err := rows.Scan(&id, &dist); err != nil {
			t.Fatalf("scan: %v", err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows.Err: %v", err)
	}
	if count != 2 {
		t.Errorf("got %d rows from KNN, want 2", count)
	}
}

// float32sToBlob serializes a float32 slice to little-endian bytes,
// which is the on-wire format sqlite-vec expects.
func float32sToBlob(v []float32) []byte {
	out := make([]byte, 4*len(v))
	for i, f := range v {
		bits := math.Float32bits(f)
		out[4*i+0] = byte(bits)
		out[4*i+1] = byte(bits >> 8)
		out[4*i+2] = byte(bits >> 16)
		out[4*i+3] = byte(bits >> 24)
	}
	return out
}
