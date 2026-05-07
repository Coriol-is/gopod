package ipc

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestPruneDirKeepsNewestN(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 250; i++ {
		// Names sort ascending; pruneDir must keep the largest 200.
		name := fmt.Sprintf("%013d-deadbe.json", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
	}

	if err := pruneDir(dir, 200); err != nil {
		t.Fatalf("pruneDir: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) != 200 {
		t.Fatalf("len = %d, want 200", len(entries))
	}
	// The 200 newest are indices 50..249.
	wantFirst := fmt.Sprintf("%013d-deadbe.json", 50)
	if entries[0].Name() != wantFirst {
		t.Errorf("first remaining = %q, want %q", entries[0].Name(), wantFirst)
	}
}

func TestPruneDirNoopBelowCap(t *testing.T) {
	dir := t.TempDir()
	for i := 0; i < 10; i++ {
		name := fmt.Sprintf("f%02d.json", i)
		os.WriteFile(filepath.Join(dir, name), []byte("{}"), 0o644)
	}
	if err := pruneDir(dir, 200); err != nil {
		t.Fatalf("pruneDir: %v", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 10 {
		t.Errorf("len = %d, want 10", len(entries))
	}
}

func TestPruneDirIgnoresSubdirs(t *testing.T) {
	dir := t.TempDir()
	if err := os.Mkdir(filepath.Join(dir, "subdir"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := pruneDir(dir, 200); err != nil {
		t.Fatalf("pruneDir: %v", err)
	}
}
