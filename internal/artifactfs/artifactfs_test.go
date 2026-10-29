//go:build linux || darwin

package artifactfs

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const (
	prepA = "11111111-1111-4111-8111-111111111111"
	artA  = "22222222-2222-4222-8222-222222222222"
)

type env struct {
	t     *testing.T
	src   string
	root  *SourceRoot
	blobs *Blobs
}

func newEnv(t *testing.T) *env {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(base, "chat")
	if err = os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := OpenSourceRoot(src)
	if err != nil {
		t.Fatal(err)
	}
	blobs, err := OpenBlobs(filepath.Join(base, "blobs"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { root.Close(); blobs.Close() })
	return &env{t: t, src: src, root: root, blobs: blobs}
}

func (e *env) write(rel, content string) string {
	e.t.Helper()
	p := filepath.Join(e.src, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		e.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		e.t.Fatal(err)
	}
	return p
}

func hexSum(s string) string { d := sha256.Sum256([]byte(s)); return hex.EncodeToString(d[:]) }

func TestOpenRejectsUnsafeSources(t *testing.T) {
	e := newEnv(t)
	e.write("ok.txt", "hello")
	e.write("dir/inner.txt", "inner")
	outside := filepath.Join(filepath.Dir(e.src), "outside.txt")
	if err := os.WriteFile(outside, []byte("secret"), 0o644); err != nil {
		t.Fatal(err)
	}
	// symlink as final component, symlink ancestor (inside and outside the root)
	must(t, os.Symlink(outside, filepath.Join(e.src, "final-link")))
	must(t, os.Symlink(filepath.Dir(e.src), filepath.Join(e.src, "up-link")))
	must(t, os.Symlink(filepath.Join(e.src, "dir"), filepath.Join(e.src, "inside-link")))
	// hardlink
	must(t, os.Link(filepath.Join(e.src, "ok.txt"), filepath.Join(e.src, "hard.txt")))
	// FIFO and socket-like specials
	must(t, unix.Mkfifo(filepath.Join(e.src, "fifo"), 0o644))
	must(t, os.Mkdir(filepath.Join(e.src, "adir"), 0o755))

	cases := []struct {
		rel  string
		want error
	}{
		{"final-link", ErrUnsafePath},
		{"up-link/outside.txt", ErrUnsafePath},
		{"inside-link/inner.txt", ErrUnsafePath},
		{"hard.txt", ErrHardlinked},
		{"ok.txt", ErrHardlinked}, // the other name of the same inode
		{"fifo", ErrNotRegular},
		{"adir", ErrNotRegular},
		{"../outside.txt", ErrUnsafePath},
		{"dir/../ok.txt", ErrUnsafePath},
		{"/ok.txt", ErrUnsafePath},
		{"dir//inner.txt", ErrUnsafePath},
		{"./ok.txt", ErrUnsafePath},
		{"", ErrUnsafePath},
		{"dir/", ErrUnsafePath},
		{"nope.txt", ErrNotFound},
		{"nodir/x.txt", ErrNotFound},
		{"a\x00b", ErrUnsafePath},
		{strings.Repeat("a/", 20) + "f", ErrUnsafePath},
	}
	for _, c := range cases {
		f, err := e.root.Open(c.rel, 1<<20)
		if err == nil {
			f.Close()
		}
		if !errors.Is(err, c.want) {
			t.Errorf("%q: got %v, want %v", c.rel, err, c.want)
		}
	}
	f, err := e.root.Open("dir/inner.txt", 1<<20)
	if err != nil {
		t.Fatalf("ordinary nested file: %v", err)
	}
	f.Close()
}

func TestOpenEnforcesSizeBound(t *testing.T) {
	e := newEnv(t)
	e.write("big.bin", strings.Repeat("x", 101))
	if _, err := e.root.Open("big.bin", 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversize: %v", err)
	}
	e.write("edge.bin", strings.Repeat("x", 100))
	f, err := e.root.Open("edge.bin", 100)
	if err != nil {
		t.Fatalf("exactly max: %v", err)
	}
	f.Close()
}

func TestSnapshotCopiesAndHashes(t *testing.T) {
	e := newEnv(t)
	e.write("r.csv", "a,b\n1,2\n")
	must(t, e.blobs.NewPreparation(prepA))
	src, err := e.root.Open("r.csv", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	snap, err := e.blobs.Snapshot(prepA, artA+".blob", src, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if snap.Size != 8 || snap.SHA256 != hexSum("a,b\n1,2\n") {
		t.Fatalf("snapshot %+v", snap)
	}
	key := prepA + "/" + artA + ".blob"
	st, err := os.Lstat(filepath.Join(e.blobs.root, key))
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("blob mode %v %v", st, err)
	}
	// source removal after the snapshot changes nothing
	must(t, os.Remove(filepath.Join(e.src, "r.csv")))
	data, err := e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 1<<20)
	if err != nil || string(data) != "a,b\n1,2\n" {
		t.Fatalf("read after source removal: %q %v", data, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.blobs.root, prepA)); len(entries) != 1 {
		t.Fatalf("leftover files: %v", entries)
	}
}

func TestSnapshotDetectsChangeDuringCopy(t *testing.T) {
	big := strings.Repeat("0123456789", 20000) // > one copy chunk
	cases := map[string]func(path string){
		"append": func(p string) {
			f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0)
			f.WriteString("more")
			f.Close()
		},
		"truncate": func(p string) { os.Truncate(p, 10) },
		"same size rewrite of an already copied region": func(p string) {
			f, _ := os.OpenFile(p, os.O_WRONLY, 0)
			f.WriteAt([]byte("ZZZZZ"), 0)
			f.Close()
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			e := newEnv(t)
			p := e.write("f.bin", big)
			must(t, e.blobs.NewPreparation(prepA))
			src, err := e.root.Open("f.bin", 1<<22)
			if err != nil {
				t.Fatal(err)
			}
			defer src.Close()
			e.blobs.Fault = func(point string) error {
				if point == "copy_mid" {
					mutate(p)
				}
				return nil
			}
			_, err = e.blobs.Snapshot(prepA, artA+".blob", src, 1<<22)
			if !errors.Is(err, ErrSourceChanged) {
				t.Fatalf("got %v, want ErrSourceChanged", err)
			}
			if entries, _ := os.ReadDir(filepath.Join(e.blobs.root, prepA)); len(entries) != 0 {
				t.Fatalf("failed snapshot left %v", entries)
			}
		})
	}
}

func TestSnapshotEnforcesBoundWhileCopying(t *testing.T) {
	e := newEnv(t)
	p := e.write("f.bin", strings.Repeat("x", 100))
	must(t, e.blobs.NewPreparation(prepA))
	src, err := e.root.Open("f.bin", 100)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	// The file grows after open: the bound is checked on bytes actually read.
	must(t, os.WriteFile(p, []byte(strings.Repeat("x", 500)), 0o644))
	if _, err = e.blobs.Snapshot(prepA, artA+".blob", src, 100); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("got %v", err)
	}
}

func TestSnapshotFaultsAtEveryBoundary(t *testing.T) {
	for _, point := range []string{"copy_mid", "copy_end", "after_file_fsync", "after_rename", "after_dir_fsync"} {
		t.Run("error/"+point, func(t *testing.T) {
			e := newEnv(t)
			e.write("f.txt", "data")
			must(t, e.blobs.NewPreparation(prepA))
			src, _ := e.root.Open("f.txt", 100)
			defer src.Close()
			boom := errors.New("injected")
			e.blobs.Fault = func(p string) error {
				if p == point {
					return boom
				}
				return nil
			}
			_, err := e.blobs.Snapshot(prepA, artA+".blob", src, 100)
			if !errors.Is(err, boom) {
				t.Fatalf("got %v", err)
			}
			entries, _ := os.ReadDir(filepath.Join(e.blobs.root, prepA))
			if point == "after_dir_fsync" {
				// Durable file exists; the caller never got facts, so the
				// inventory abandons the preparation and reconcile removes it.
				if len(entries) != 1 {
					t.Fatalf("expected the durable blob, got %v", entries)
				}
			} else if len(entries) != 0 {
				t.Fatalf("ordinary failure must clean up, left %v", entries)
			}
		})
		t.Run("crash/"+point, func(t *testing.T) {
			e := newEnv(t)
			e.write("f.txt", "data")
			must(t, e.blobs.NewPreparation(prepA))
			src, _ := e.root.Open("f.txt", 100)
			defer src.Close()
			e.blobs.Fault = func(p string) error {
				if p == point {
					return ErrSimulatedCrash
				}
				return nil
			}
			if _, err := e.blobs.Snapshot(prepA, artA+".blob", src, 100); !errors.Is(err, ErrSimulatedCrash) {
				t.Fatalf("got %v", err)
			}
			// After a crash the directory is removable and nothing else is.
			must(t, e.blobs.RemovePreparation(prepA))
			must(t, e.blobs.RemovePreparation(prepA)) // idempotent
			if l, _ := e.blobs.ListPreparations(); len(l) != 0 {
				t.Fatalf("remaining %v", l)
			}
		})
	}
}

func TestSnapshotDiskFullIsTyped(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "data")
	must(t, e.blobs.NewPreparation(prepA))
	src, _ := e.root.Open("f.txt", 100)
	defer src.Close()
	e.blobs.Fault = func(p string) error {
		if p == "copy_mid" {
			return mapErrno(syscall.ENOSPC)
		}
		return nil
	}
	if _, err := e.blobs.Snapshot(prepA, artA+".blob", src, 100); !errors.Is(err, ErrStorageFull) {
		t.Fatalf("got %v", err)
	}
	if entries, _ := os.ReadDir(filepath.Join(e.blobs.root, prepA)); len(entries) != 0 {
		t.Fatalf("left %v", entries)
	}
}

func TestReadVerifiedRejectsTamperingAndLinks(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "payload")
	must(t, e.blobs.NewPreparation(prepA))
	src, _ := e.root.Open("f.txt", 100)
	snap, err := e.blobs.Snapshot(prepA, artA+".blob", src, 100)
	src.Close()
	if err != nil {
		t.Fatal(err)
	}
	key := prepA + "/" + artA + ".blob"
	path := filepath.Join(e.blobs.root, key)

	if _, err = e.blobs.ReadVerified(key, snap.Size, hexSum("other"), 100); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong hash: %v", err)
	}
	if _, err = e.blobs.ReadVerified(key, snap.Size+1, snap.SHA256, 100); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("wrong size: %v", err)
	}
	if _, err = e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 3); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("over bound: %v", err)
	}
	for _, bad := range []string{"../x", prepA + "/../" + artA + ".blob", prepA + "/x.blob", "x/" + artA + ".blob", ""} {
		if _, err = e.blobs.ReadVerified(bad, 1, hexSum("x"), 100); !errors.Is(err, ErrUnsafePath) {
			t.Fatalf("key %q: %v", bad, err)
		}
	}
	// same size, different bytes
	must(t, os.WriteFile(path, []byte("PAYLOAD"), 0o600))
	if _, err = e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 100); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("tampered: %v", err)
	}
	// replaced by a symlink to an outside file with the right bytes
	outside := filepath.Join(filepath.Dir(e.src), "outside")
	must(t, os.WriteFile(outside, []byte("payload"), 0o600))
	must(t, os.Remove(path))
	must(t, os.Symlink(outside, path))
	if _, err = e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 100); err == nil {
		t.Fatal("symlinked blob was read")
	}
	must(t, os.Remove(path))
	if _, err = e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 100); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing: %v", err)
	}
	if _, err = e.blobs.StatBlob(key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("stat missing: %v", err)
	}
	// A hard link inside the private store is harmless: the hash decides.
	must(t, os.WriteFile(path, []byte("payload"), 0o600))
	must(t, os.Link(path, filepath.Join(filepath.Dir(path), "x.link")))
	if data, err := e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 100); err != nil || string(data) != "payload" {
		t.Fatalf("hardlinked blob with correct content must verify: %q %v", data, err)
	}
	must(t, os.WriteFile(path, []byte("PAYLOAD"), 0o600)) // shared inode: both names change
	if _, err = e.blobs.ReadVerified(key, snap.Size, snap.SHA256, 100); !errors.Is(err, ErrCorrupt) {
		t.Fatalf("hardlinked blob with wrong content: %v", err)
	}
}

func TestPreparationSymlinkIsNotFollowed(t *testing.T) {
	e := newEnv(t)
	outside := filepath.Join(filepath.Dir(e.src), "elsewhere")
	must(t, os.MkdirAll(outside, 0o700))
	must(t, os.WriteFile(filepath.Join(outside, artA+".blob"), []byte("x"), 0o600))
	must(t, os.Symlink(outside, filepath.Join(e.blobs.root, prepA)))
	if _, err := e.blobs.StatBlob(prepA + "/" + artA + ".blob"); err == nil {
		t.Fatal("followed a symlinked preparation directory")
	}
	if err := e.blobs.RemovePreparation(prepA); err == nil {
		t.Fatal("removed through a symlinked preparation directory")
	}
	if _, err := os.Stat(filepath.Join(outside, artA+".blob")); err != nil {
		t.Fatalf("outside file touched: %v", err)
	}
	// ListPreparations only reports real directories.
	if l, _ := e.blobs.ListPreparations(); len(l) != 0 {
		t.Fatalf("listed %v", l)
	}
}

func TestOpenBlobsRequiresPrivateRoot(t *testing.T) {
	base := t.TempDir()
	loose := filepath.Join(base, "loose")
	must(t, os.Mkdir(loose, 0o755))
	if _, err := OpenBlobs(loose); !errors.Is(err, ErrUnsafeRoot) {
		t.Fatalf("0755 root: %v", err)
	}
}

func TestRemoveBlobIsIdempotentAndKeepsSiblings(t *testing.T) {
	e := newEnv(t)
	e.write("a.txt", "a")
	e.write("b.txt", "b")
	must(t, e.blobs.NewPreparation(prepA))
	artB := "33333333-3333-4333-8333-333333333333"
	for name, id := range map[string]string{"a.txt": artA, "b.txt": artB} {
		src, _ := e.root.Open(name, 10)
		if _, err := e.blobs.Snapshot(prepA, id+".blob", src, 10); err != nil {
			t.Fatal(err)
		}
		src.Close()
	}
	keyA := prepA + "/" + artA + ".blob"
	must(t, e.blobs.RemoveBlob(keyA))
	must(t, e.blobs.RemoveBlob(keyA))
	if names, _ := e.blobs.PreparationBlobs(prepA); len(names) != 1 {
		t.Fatalf("sibling lost: %v", names)
	}
	must(t, e.blobs.RemoveBlob(prepA+"/"+artB+".blob"))
	if l, _ := e.blobs.ListPreparations(); len(l) != 0 {
		t.Fatalf("empty preparation directory remains: %v", l)
	}
}

func TestFreeBytes(t *testing.T) {
	e := newEnv(t)
	if n, err := e.blobs.FreeBytes(); err != nil || n == 0 {
		t.Fatalf("free %d %v", n, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestListPreparationsIsRepeatable(t *testing.T) {
	e := newEnv(t)
	must(t, e.blobs.NewPreparation(prepA))
	for i := 0; i < 3; i++ {
		if l, err := e.blobs.ListPreparations(); err != nil || len(l) != 1 {
			t.Fatalf("call %d: %v %v", i, l, err)
		}
	}
}

// The metadata check (ctime cannot be restored) usually fires first, so the
// content re-read is tested on its own: same size, mtime put back, only the
// bytes differ.
func TestRehashContentCatchesRewriteWithRestoredMtime(t *testing.T) {
	e := newEnv(t)
	p := e.write("f.bin", strings.Repeat("a", 5000))
	src, err := e.root.Open("f.bin", 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer src.Close()
	want := hexSum(strings.Repeat("a", 5000))
	if err = rehashContent(src, want); err != nil {
		t.Fatalf("unchanged source: %v", err)
	}
	st, _ := os.Stat(p)
	f, _ := os.OpenFile(p, os.O_WRONLY, 0)
	f.WriteAt([]byte("Z"), 0)
	f.Close()
	must(t, os.Chtimes(p, st.ModTime(), st.ModTime()))
	if err = rehashContent(src, want); !errors.Is(err, ErrSourceChanged) {
		t.Fatalf("rewrite with restored mtime: %v", err)
	}
}

func TestMissingPreparationIsAHostFaultNotASourceError(t *testing.T) {
	e := newEnv(t)
	e.write("f.txt", "data")
	src, _ := e.root.Open("f.txt", 100)
	defer src.Close()
	_, err := e.blobs.Snapshot(prepA, artA+".blob", src, 100) // preparation never created
	if !errors.Is(err, ErrPreparationMissing) || errors.Is(err, ErrNotFound) {
		t.Fatalf("got %v", err)
	}
}
