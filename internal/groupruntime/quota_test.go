package groupruntime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func mustWrite(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}

func wantReason(t *testing.T, err error, reason TreeReason) *TreeBudgetError {
	t.Helper()
	var tb *TreeBudgetError
	if !errors.As(err, &tb) || tb.Reason != reason {
		t.Fatalf("want reason %q, got %v", reason, err)
	}
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("typed error must still match ErrUnavailable: %v", err)
	}
	return tb
}

// codexTree lays out what pinned Codex leaves in CODEX_HOME after turns
// (observed locally with 0.165.0; layout documented in the CP-020 ADR).
func codexTree(t *testing.T, root string) {
	t.Helper()
	base := filepath.Join(root, "codex")
	for _, db := range []string{"state_5", "logs_2", "thread_history_1", "goals_1", "memories_1", "queue_1"} {
		mustWrite(t, filepath.Join(base, db+".sqlite"), 40<<10)
		mustWrite(t, filepath.Join(base, db+".sqlite-wal"), 4<<20)
		mustWrite(t, filepath.Join(base, db+".sqlite-shm"), 32<<10)
	}
	mustWrite(t, filepath.Join(base, "config.toml"), 0)
	mustWrite(t, filepath.Join(base, "auth.json"), 0)
	mustWrite(t, filepath.Join(base, "installation_id"), 36)
	mustWrite(t, filepath.Join(base, "models_cache.json"), 460<<10)
	mustWrite(t, filepath.Join(base, "thread-writer-locks", ".coordination.lock"), 0)
	for i := 0; i < 300; i++ {
		mustWrite(t, filepath.Join(base, "sessions", "2026", "10", fmt.Sprintf("%02d", i%28+1), fmt.Sprintf("rollout-%d.jsonl", i)), 2<<10)
	}
	for i := 0; i < 60; i++ {
		mustWrite(t, filepath.Join(base, "skills", ".system", fmt.Sprintf("s%d", i%6), "assets", fmt.Sprintf("f%d", i)), 1<<10)
	}
}

func addArg0(t *testing.T, root string, names ...string) {
	t.Helper()
	dir := filepath.Join(root, "codex", "tmp", "arg0", "codex-arg0AbC123")
	mustWrite(t, filepath.Join(dir, ".lock"), 0)
	for _, n := range names {
		if err := os.Symlink("/usr/local/lib/node_modules/@openai/codex/bin/codex", filepath.Join(dir, n)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTreeBudgetAcceptsRealisticCodexState(t *testing.T) {
	root := t.TempDir()
	codexTree(t, root)
	if err := privateTreeBudget(context.Background(), root, nativeTreePolicy); err != nil {
		t.Fatalf("realistic codex state rejected: %v", err)
	}
}

func TestTreeBudgetAllowsOnlyCodexArg0Aliases(t *testing.T) {
	root := t.TempDir()
	codexTree(t, root)
	addArg0(t, root, "apply_patch", "applypatch", "codex-linux-sandbox", "codex-execve-wrapper")
	if err := privateTreeBudget(context.Background(), root, nativeTreePolicy); err != nil {
		t.Fatalf("pinned codex arg0 aliases rejected: %v", err)
	}
	// The same layout is not tolerated in a tree without the exemption.
	strict := nativeTreePolicy
	strict.allowSymlink = nil
	wantReason(t, privateTreeBudget(context.Background(), root, strict), TreeSymlink)
}

func TestTreeBudgetArg0ExemptionIsNarrow(t *testing.T) {
	cases := map[string]func(t *testing.T, root string){
		"unknown alias name": func(t *testing.T, root string) { addArg0(t, root, "evil") },
		"wrong parent": func(t *testing.T, root string) {
			mustWrite(t, filepath.Join(root, "codex", "sessions", "x"), 1)
			if err := os.Symlink("/etc/passwd", filepath.Join(root, "codex", "sessions", "apply_patch")); err != nil {
				t.Fatal(err)
			}
		},
		"nested deeper": func(t *testing.T, root string) {
			dir := filepath.Join(root, "codex", "tmp", "arg0", "codex-arg0AbC123", "sub")
			mustWrite(t, filepath.Join(dir, "x"), 1)
			if err := os.Symlink("/etc", filepath.Join(dir, "apply_patch")); err != nil {
				t.Fatal(err)
			}
		},
		"bad dir prefix": func(t *testing.T, root string) {
			dir := filepath.Join(root, "codex", "tmp", "arg0", "other")
			mustWrite(t, filepath.Join(dir, "x"), 1)
			if err := os.Symlink("/etc", filepath.Join(dir, "apply_patch")); err != nil {
				t.Fatal(err)
			}
		},
		"symlinked directory": func(t *testing.T, root string) {
			if err := os.MkdirAll(filepath.Join(root, "codex", "tmp", "arg0"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("/etc", filepath.Join(root, "codex", "tmp", "arg0", "codex-arg0Zzz")); err != nil {
				t.Fatal(err)
			}
		},
		"non-arg0 tmp": func(t *testing.T, root string) {
			mustWrite(t, filepath.Join(root, "codex", "tmp", "x"), 1)
			if err := os.Symlink("/etc", filepath.Join(root, "codex", "tmp", "apply_patch")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, plant := range cases {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			plant(t, root)
			wantReason(t, privateTreeBudget(context.Background(), root, nativeTreePolicy), TreeSymlink)
		})
	}
}

func TestTreeBudgetRejectsSymlinkEverywhereElse(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a", "b"), 1)
	if err := os.Symlink("/etc/hostname", filepath.Join(root, "a", "link")); err != nil {
		t.Fatal(err)
	}
	tb := wantReason(t, privateTreeBudget(context.Background(), root, workspaceTreePolicy), TreeSymlink)
	if tb.Class != "a/…" && tb.Class != "a/link" {
		t.Fatalf("unexpected class %q", tb.Class)
	}
}

func TestTreeBudgetRejectsFIFOAndSocket(t *testing.T) {
	root := t.TempDir()
	if err := syscall.Mkfifo(filepath.Join(root, "pipe"), 0o600); err != nil {
		t.Fatal(err)
	}
	wantReason(t, privateTreeBudget(context.Background(), root, workspaceTreePolicy), TreeSpecialFile)

	short, err := os.MkdirTemp("/tmp", "tb")
	if err != nil {
		t.Skip("no short temp dir for unix socket")
	}
	defer os.RemoveAll(short)
	l, err := net.Listen("unix", filepath.Join(short, "s"))
	if err != nil {
		t.Skip("unix sockets unavailable")
	}
	defer l.Close()
	wantReason(t, privateTreeBudget(context.Background(), short, workspaceTreePolicy), TreeSpecialFile)
}

func TestTreeBudgetDepthCountsPathComponents(t *testing.T) {
	deep := func(n int) string {
		parts := make([]string, n)
		for i := range parts {
			parts[i] = "d"
		}
		return filepath.Join(parts...)
	}
	ok := t.TempDir()
	if err := os.MkdirAll(filepath.Join(ok, deep(maxTreeDepth-1)), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(ok, deep(maxTreeDepth-1), "f"), 1)
	if err := privateTreeBudget(context.Background(), ok, workspaceTreePolicy); err != nil {
		t.Fatalf("depth at limit rejected: %v", err)
	}
	bad := t.TempDir()
	mustWrite(t, filepath.Join(bad, deep(maxTreeDepth+1), "f"), 1)
	wantReason(t, privateTreeBudget(context.Background(), bad, workspaceTreePolicy), TreeDepth)
}

func TestTreeBudgetBytesAndFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "big"), 2<<10)
	wantReason(t, privateTreeBudget(context.Background(), root, treePolicy{Label: "t", Bytes: 1 << 10, Files: 10}), TreeBytes)
	many := t.TempDir()
	for i := 0; i < 11; i++ {
		mustWrite(t, filepath.Join(many, fmt.Sprintf("f%d", i)), 1)
	}
	wantReason(t, privateTreeBudget(context.Background(), many, treePolicy{Label: "t", Bytes: 1 << 20, Files: 10}), TreeFiles)
}

func TestTreeBudgetWalkFailures(t *testing.T) {
	wantReason(t, privateTreeBudget(context.Background(), filepath.Join(t.TempDir(), "missing"), workspaceTreePolicy), TreeWalk)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "f"), 1)
	wantReason(t, privateTreeBudget(ctx, root, workspaceTreePolicy), TreeWalk)
}

// A directory listed as a regular file/dir that becomes a symlink before it is
// examined must fail closed, never be followed.
func TestTreeBudgetFailsClosedWhenEntrySwappedForSymlink(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	mustWrite(t, filepath.Join(outside, "huge"), 1<<20)
	mustWrite(t, filepath.Join(root, "a"), 1)
	mustWrite(t, filepath.Join(root, "b"), 1)
	if err := os.MkdirAll(filepath.Join(root, "c"), 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "c", "x"), 1)
	policy := workspaceTreePolicy
	policy.afterList = func(rel string) {
		for _, name := range []string{"b", "c"} {
			p := filepath.Join(root, name)
			if err := os.RemoveAll(p); err != nil {
				t.Error(err)
			}
			if err := os.Symlink(outside, p); err != nil {
				t.Error(err)
			}
		}
	}
	wantReason(t, privateTreeBudget(context.Background(), root, policy), TreeSymlink)
}

func TestTreeBudgetBoundedUnderConcurrentGrowth(t *testing.T) {
	root := t.TempDir()
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			_ = os.WriteFile(filepath.Join(root, fmt.Sprintf("g%d", i)), []byte("x"), 0o600)
			_ = os.MkdirAll(filepath.Join(root, fmt.Sprintf("d%d", i)), 0o700)
		}
	}()
	done := make(chan error, 1)
	go func() {
		done <- privateTreeBudget(context.Background(), root, treePolicy{Label: "t", Bytes: 1 << 20, Files: 500})
	}()
	select {
	case err := <-done:
		if err != nil {
			wantReason(t, err, TreeFiles)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("walk did not terminate under concurrent growth")
	}
	close(stop)
	wg.Wait()
}

func TestTreeBudgetErrorIsSanitized(t *testing.T) {
	root := t.TempDir()
	hostile := "a\nb token=SECRET\x1b[31m" + strings.Repeat("z", 80)
	mustWrite(t, filepath.Join(root, hostile, "inner"), 1)
	if err := os.Symlink("/etc", filepath.Join(root, hostile, "link")); err != nil {
		t.Fatal(err)
	}
	tb := wantReason(t, privateTreeBudget(context.Background(), root, workspaceTreePolicy), TreeSymlink)
	text := tb.Error() + " " + tb.Detail()
	for _, bad := range []string{"\n", "\x1b", " token", "SECRET="} {
		if strings.Contains(text, bad) {
			t.Fatalf("unsanitized diagnostic %q", text)
		}
	}
	if len(tb.Class) > 64 {
		t.Fatalf("class not truncated: %d", len(tb.Class))
	}
}

func TestAdmissionErrorCarriesTreeDetail(t *testing.T) {
	cause := &TreeBudgetError{Tree: "native", Reason: TreeSymlink, Class: "codex/tmp"}
	got := newAdmissionError("configuration", cause)
	if got.Stage != "configuration" || got.Detail != "tree=native reason=symlink class=codex/tmp" || !errors.Is(got, ErrUnavailable) {
		t.Fatalf("unexpected admission error: %+v", got)
	}
	if plain := newAdmissionError("admission", ErrUnavailable); plain.Detail != "" {
		t.Fatalf("detail leaked for non-tree cause: %q", plain.Detail)
	}
}
