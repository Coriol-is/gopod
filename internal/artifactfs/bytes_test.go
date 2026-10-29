//go:build linux || darwin

package artifactfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestSnapshotBytesIsPrivateStableAndVerifiable(t *testing.T) {
	b := newEnv(t).blobs
	prep, name := prepA, artA+".blob"
	if err := b.NewPreparation(prep); err != nil {
		t.Fatal(err)
	}
	data := []byte("package main\n")
	snap, err := b.SnapshotBytes(prep, name, data, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	if snap.SHA256 != hex.EncodeToString(sum[:]) || snap.Size != int64(len(data)) {
		t.Fatalf("snapshot = %+v", snap)
	}
	// The caller mutating its buffer afterwards cannot change the stored bytes.
	data[0] = 'X'
	got, err := b.ReadVerified(prep+"/"+name, snap.Size, snap.SHA256, 1<<20)
	if err != nil || string(got) != "package main\n" {
		t.Fatalf("ReadVerified = %q, %v", got, err)
	}
	st, err := os.Stat(filepath.Join(b.RootForTest(), prep, name))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %v, %v", st.Mode(), err)
	}
	if _, err := b.SnapshotBytes(prep, name, []byte("again"), 1<<20); err == nil {
		t.Fatal("overwrite must fail")
	}
}

func TestSnapshotBytesBoundsAndFaults(t *testing.T) {
	b := newEnv(t).blobs
	prep, name := prepA, artA+".blob"
	if err := b.NewPreparation(prep); err != nil {
		t.Fatal(err)
	}
	if _, err := b.SnapshotBytes(prep, name, make([]byte, 11), 10); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("over bound = %v", err)
	}
	if _, err := b.SnapshotBytes(prep, "../x", []byte("a"), 10); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("bad name = %v", err)
	}
	b.Fault = func(p string) error {
		if p == "after_file_fsync" {
			return errors.New("injected")
		}
		return nil
	}
	if _, err := b.SnapshotBytes(prep, name, []byte("a"), 10); err == nil {
		t.Fatal("fault must fail")
	}
	// A failed attempt leaves neither the part file nor the final name.
	ents, _ := os.ReadDir(filepath.Join(b.RootForTest(), prep))
	if len(ents) != 0 {
		t.Fatalf("leftovers: %v", ents)
	}
}
