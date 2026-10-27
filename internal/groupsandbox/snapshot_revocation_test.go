package groupsandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type revokingReader struct {
	io.Reader
	revoke func()
}

func (r *revokingReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	if n > 0 {
		r.revoke()
	}
	return n, err
}

func TestSnapshotRevocationDuringPreparationPreservesCommittedBytes(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	e := &Executor{Root: root, UID: 1000, GID: 1000}
	old := tarBytes(&tar.Header{Name: "chat/proof", Typeflag: tar.TypeReg, Mode: 0600, Size: 3}, "old")
	if err := e.save("namespace-one", bytes.NewReader(old)); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "namespace-one", "snapshot.tar")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	next := tarBytes(&tar.Header{Name: "chat/proof", Typeflag: tar.TypeReg, Mode: 0600, Size: 3}, "new")
	revoked := false
	reader := &revokingReader{Reader: bytes.NewReader(next), revoke: func() { revoked = true }}
	err = e.saveChecked(context.Background(), "namespace-one", reader, func(context.Context) error {
		if revoked {
			return errors.New("revoked")
		}
		return nil
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("revoked save admitted: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("revocation replaced previous snapshot")
	}
	temps, err := filepath.Glob(filepath.Join(root, "namespace-one", ".snapshot-*"))
	if err != nil || len(temps) != 0 {
		t.Fatal("prepared snapshot was not cleaned")
	}
	// A permitted replacement is the positive control for the same path.
	if err = e.saveChecked(context.Background(), "namespace-one", bytes.NewReader(next), func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	current, err := e.snapshot("namespace-one")
	if err != nil || !bytes.Contains(current, []byte("new")) {
		t.Fatal("permitted snapshot replacement failed")
	}
}
