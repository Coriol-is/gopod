package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
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
	before, err := os.ReadFile(snapshot)
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
	if err = e.ImportUploads(context.Background(), "namespace-one", workspace, "trusted guide", func(context.Context) error { return nil }); err == nil {
		t.Fatal("aggregate >8MiB admitted")
	}
	after, err := os.ReadFile(snapshot)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("budget failure replaced previous snapshot")
	}
	if err = os.Remove(filepath.Join(uploads, "three")); err != nil {
		t.Fatal(err)
	}
	if err = e.ImportUploads(context.Background(), "namespace-one", workspace, "trusted guide", func(context.Context) error { return nil }); err != nil {
		t.Fatal("bounded positive control failed", err)
	}
}
