package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestImportUploadsEnforcesAggregateBudgetBeforeCommit(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	e := &Executor{Root: root, UID: 1000, GID: 1000}
	old := tarBytes(&tar.Header{Name: "proof", Typeflag: tar.TypeReg, Mode: 0600, Size: 3}, "old")
	if err := e.save("namespace-one", bytes.NewReader(old)); err != nil {
		t.Fatal(err)
	}
	snapshot := filepath.Join(root, "namespace-one", "snapshot.tar")
	_, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	uploads := filepath.Join(workspace, "uploads")
	if err = os.Mkdir(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one", "two", "three"} {
		f, err := os.Create(filepath.Join(uploads, name))
		if err != nil {
			t.Fatal(err)
		}
		err = f.Truncate(3 << 20)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = e.ImportUploads(context.Background(), "namespace-one", workspace, "trusted guide", func(context.Context) error { return nil }); err != nil {
		t.Fatal("overflow should skip inputs, not disable the group", err)
	}
	after, err := os.ReadFile(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(after))
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg && h.Size == 3<<20 {
			count++
		}
	}
	if count != 2 {
		t.Fatalf("bounded upload window has %d large inputs", count)
	}

	if err = os.Remove(filepath.Join(uploads, "three")); err != nil {
		t.Fatal(err)
	}
	if err = e.ImportUploads(context.Background(), "namespace-one", workspace, "trusted guide", func(context.Context) error { return nil }); err != nil {
		t.Fatal("bounded positive control failed", err)
	}
}

func TestImportUploadCountOverflowDoesNotDisableGroup(t *testing.T) {
	root := t.TempDir()
	_ = os.Chmod(root, 0700)
	e := &Executor{Root: root, UID: 1000, GID: 1000}
	workspace := t.TempDir()
	uploads := filepath.Join(workspace, "uploads")
	if err := os.Mkdir(uploads, 0700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 140; i++ {
		if err := os.WriteFile(filepath.Join(uploads, fmt.Sprintf("%03d", i)), []byte("safe"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for range 2 {
		if err := e.ImportUploads(t.Context(), "namespace-one", workspace, "guide", func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
	}
	seed, err := e.snapshot("namespace-one")
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(bytes.NewReader(seed))
	count := 0
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			count++
		}
	}
	if count != 129 {
		t.Fatalf("expected 128 uploads and guide, got %d", count)
	}
}
