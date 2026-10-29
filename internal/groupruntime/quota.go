package groupruntime

import (
	"context"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"
)

// TreeReason is a fixed, non-sensitive code explaining why a private tree was
// refused. It is safe for host logs and owner-private diagnostics; it must never
// be forwarded to a group chat.
type TreeReason string

const (
	TreeSymlink     TreeReason = "symlink"
	TreeSpecialFile TreeReason = "special_file"
	TreeDepth       TreeReason = "depth"
	TreeBytes       TreeReason = "bytes"
	TreeFiles       TreeReason = "files"
	TreeWalk        TreeReason = "walk"
)

const (
	maxTreeDepth   = 64
	treeBatch      = 256
	maxClassLength = 24
	classParts     = 2
)

// TreeBudgetError identifies the refused tree and the class of the offending
// path. Class keeps at most two sanitized, truncated components; file names
// from untrusted content never appear verbatim.
type TreeBudgetError struct {
	Tree   string
	Reason TreeReason
	Class  string
}

func (e *TreeBudgetError) Error() string {
	return "group tree budget: " + e.Detail()
}
func (e *TreeBudgetError) Unwrap() error { return ErrUnavailable }

// Detail is the fixed key=value form used by logs and owner diagnostics.
func (e *TreeBudgetError) Detail() string {
	return "tree=" + e.Tree + " reason=" + string(e.Reason) + " class=" + e.Class
}

// treePolicy bounds one private tree. allowSymlink exempts a specific trusted,
// host-known path (never a pattern reachable by untrusted code); afterList is a
// test hook invoked after a directory has been listed.
type treePolicy struct {
	Label        string
	Bytes        int64
	Files        int
	allowSymlink func(rel string) bool
	afterList    func(rel string)
}

var (
	// Pinned Codex keeps per-process alias symlinks under CODEX_HOME/tmp/arg0
	// and leaves them behind after the process exits; its own janitor removes
	// stale ones on the next start. The native container runs only that trusted
	// binary (no file tools), so the alias entries are the one tolerated symlink.
	nativeTreePolicy    = treePolicy{Label: "native", Bytes: 64 << 20, Files: 2048, allowSymlink: codexArg0AliasEntry}
	workspaceTreePolicy = treePolicy{Label: "workspace", Bytes: 32 << 20, Files: 256}
	policyTreePolicy    = treePolicy{Label: "policy", Bytes: 8 << 20, Files: 128}
)

var (
	codexArg0Dir    = regexp.MustCompile(`^codex-arg0[A-Za-z0-9]{1,16}$`)
	codexArg0Names  = map[string]bool{"apply_patch": true, "applypatch": true, "codex-linux-sandbox": true, "codex-execve-wrapper": true}
	classUnsafeRune = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// codexArg0AliasEntry matches exactly codex/tmp/arg0/codex-arg0XXXX/<alias>.
func codexArg0AliasEntry(rel string) bool {
	parts := strings.Split(rel, "/")
	return len(parts) == 5 && parts[0] == "codex" && parts[1] == "tmp" && parts[2] == "arg0" &&
		codexArg0Dir.MatchString(parts[3]) && codexArg0Names[parts[4]]
}

// privateTreeBudget walks a host-owned tree through a rooted, no-follow
// traversal. Native clients are trusted pinned executables with no file/code
// tools; user commands run elsewhere with stricter tmpfs/RLIMIT bounds.
func privateTreeBudget(ctx context.Context, dir string, p treePolicy) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return &TreeBudgetError{Tree: p.Label, Reason: TreeWalk, Class: "root"}
	}
	defer root.Close()
	w := &treeWalker{ctx: ctx, root: root, p: p}
	return w.dir("", 0)
}

type treeWalker struct {
	ctx     context.Context
	root    *os.Root
	p       treePolicy
	bytes   int64
	files   int
	entries int
}

func (w *treeWalker) fail(reason TreeReason, rel string) error {
	return &TreeBudgetError{Tree: w.p.Label, Reason: reason, Class: pathClass(rel)}
}

// openDir opens rel only if it is a real directory right now: Lstat rejects a
// symlink, and the opened handle must be the same inode (closes the swap race).
func (w *treeWalker) openDir(rel string) (*os.File, error) {
	name := rel
	if name == "" {
		name = "."
	}
	before, err := w.root.Lstat(name)
	if err != nil {
		return nil, w.fail(TreeWalk, rel)
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return nil, w.fail(TreeSymlink, rel)
	}
	if !before.IsDir() {
		return nil, w.fail(TreeSpecialFile, rel)
	}
	f, err := w.root.Open(name)
	if err != nil {
		return nil, w.fail(TreeWalk, rel)
	}
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		_ = f.Close()
		return nil, w.fail(TreeSymlink, rel)
	}
	return f, nil
}

func (w *treeWalker) dir(rel string, depth int) error {
	f, err := w.openDir(rel)
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		batch, rerr := f.ReadDir(treeBatch)
		if w.p.afterList != nil && len(batch) > 0 {
			w.p.afterList(rel)
		}
		for _, e := range batch {
			if err := w.entry(path.Join(rel, e.Name()), depth+1); err != nil {
				return err
			}
		}
		if rerr == io.EOF {
			return nil
		}
		if rerr != nil {
			return w.fail(TreeWalk, rel)
		}
	}
}

func (w *treeWalker) entry(rel string, depth int) error {
	if w.ctx.Err() != nil {
		return w.fail(TreeWalk, rel)
	}
	w.entries++
	if depth > maxTreeDepth {
		return w.fail(TreeDepth, rel)
	}
	// Directories count toward a generous bound so a looping or growing tree
	// cannot keep the walk alive; real trees stay far below it.
	if w.entries > 2*w.p.Files+maxTreeDepth {
		return w.fail(TreeFiles, rel)
	}
	info, err := w.root.Lstat(rel)
	if err != nil {
		return w.fail(TreeWalk, rel)
	}
	return w.classify(rel, depth, info)
}

func (w *treeWalker) classify(rel string, depth int, info fs.FileInfo) error {
	mode := info.Mode()
	switch {
	case mode&os.ModeSymlink != 0:
		if w.p.allowSymlink == nil || !w.p.allowSymlink(rel) {
			return w.fail(TreeSymlink, rel)
		}
		return w.count(rel, 0)
	case mode.IsDir():
		return w.dir(rel, depth)
	case mode.IsRegular():
		return w.count(rel, info.Size())
	default:
		return w.fail(TreeSpecialFile, rel)
	}
}

func (w *treeWalker) count(rel string, size int64) error {
	w.bytes += size
	w.files++
	if w.bytes > w.p.Bytes {
		return w.fail(TreeBytes, rel)
	}
	if w.files > w.p.Files {
		return w.fail(TreeFiles, rel)
	}
	return nil
}

// pathClass keeps at most classParts sanitized, truncated components and marks
// deeper paths with an ellipsis. Hostile names cannot inject log content.
func pathClass(rel string) string {
	if rel == "" {
		return "."
	}
	parts := strings.Split(rel, "/")
	out := make([]string, 0, classParts+1)
	for i, part := range parts {
		if i == classParts {
			out = append(out, "…")
			break
		}
		part = classUnsafeRune.ReplaceAllString(part, "_")
		if len(part) > maxClassLength {
			part = part[:maxClassLength]
		}
		out = append(out, part)
	}
	return strings.Join(out, "/") + depthSuffix(len(parts))
}

func depthSuffix(n int) string {
	if n <= classParts {
		return ""
	}
	return "#d" + strconv.Itoa(n)
}
