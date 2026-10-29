package groupruntime

import (
	"context"
	"errors"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
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
	TreeHeal        TreeReason = "heal"
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
// host-known path (never a pattern reachable by untrusted code). When heal is
// set, a symlink or special file is unlinked in place (never followed) and
// reported to onHeal instead of refusing admission. afterList and beforeHeal
// are test hooks.
type treePolicy struct {
	Label        string
	Bytes        int64
	Files        int
	heal         bool
	checkOnly    bool
	onHeal       func(reason TreeReason, class string)
	allowSymlink func(rel, target string) bool
	afterList    func(rel string)
	beforeHeal   func(rel string)
}

var (
	// Byte and file limits are runaway-growth guards for owner-run groups, not
	// security limits; the depth cap, no-follow traversal and removal of planted
	// links/special files are the boundary. Policy is host-written: no healing.
	//
	// Pinned Codex keeps per-process alias symlinks under CODEX_HOME/tmp/arg0
	// and leaves them behind after the process exits; its own janitor removes
	// stale ones on the next start. The native container runs only that trusted
	// binary (no file tools), so the alias entries are the one tolerated symlink.
	nativeTreePolicy    = treePolicy{Label: "native", Bytes: 512 << 20, Files: 20000, heal: true, allowSymlink: codexArg0AliasEntry}
	workspaceTreePolicy = treePolicy{Label: "workspace", Bytes: 256 << 20, Files: 5000, heal: true}
	// Authorize is a predicate that runs without the namespace lock and while
	// an agent may be live: it never mutates, healable entries are admitted.
	workspaceCheckPolicy = treePolicy{Label: "workspace", Bytes: 256 << 20, Files: 5000, heal: true, checkOnly: true}
	policyTreePolicy     = treePolicy{Label: "policy", Bytes: 8 << 20, Files: 128}
)

var (
	codexArg0Dir    = regexp.MustCompile(`^codex-arg0[A-Za-z0-9]{1,16}$`)
	codexArg0Names  = map[string]bool{"apply_patch": true, "applypatch": true, "codex-linux-sandbox": true, "codex-execve-wrapper": true}
	classUnsafeRune = regexp.MustCompile(`[^A-Za-z0-9._-]`)
)

// codexInstallPrefix is where container/group/Dockerfile.codex installs the
// pinned CLI (`npm install -g` under the node image's /usr/local prefix).
// Codex points its aliases at its own resolved executable inside this prefix.
const codexInstallPrefix = "/usr/local/lib/node_modules/@openai/"

// codexArg0AliasEntry matches exactly codex/tmp/arg0/codex-arg0XXXX/<alias>
// whose target is a cleaned absolute path to a `codex` binary in the install
// prefix. Anything else is an ordinary planted symlink.
func codexArg0AliasEntry(rel, target string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) != 5 || parts[0] != "codex" || parts[1] != "tmp" || parts[2] != "arg0" ||
		!codexArg0Dir.MatchString(parts[3]) || !codexArg0Names[parts[4]] {
		return false
	}
	return path.Clean(target) == target && strings.HasPrefix(target, codexInstallPrefix) && path.Base(target) == "codex"
}

// privateTreeBudget walks a host-owned tree with directory file descriptors
// only: the root is opened O_NOFOLLOW|O_DIRECTORY, every child is examined with
// fstatat(AT_SYMLINK_NOFOLLOW) on its verified parent handle and every
// subdirectory is opened with openat(O_NOFOLLOW). Nothing is resolved by path.
// Native clients are trusted pinned executables with no file/code tools; user
// commands run elsewhere with stricter tmpfs/RLIMIT bounds.
func privateTreeBudget(ctx context.Context, dir string, p treePolicy) error {
	fd, err := unix.Open(dir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		reason := TreeWalk
		if errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR) {
			reason = TreeSymlink
		}
		return &TreeBudgetError{Tree: p.Label, Reason: reason, Class: "root"}
	}
	root := os.NewFile(uintptr(fd), "tree")
	defer root.Close()
	w := &treeWalker{ctx: ctx, p: p}
	return w.dir(root, "", 0)
}

type treeWalker struct {
	ctx     context.Context
	p       treePolicy
	bytes   int64
	files   int
	entries int
}

func (w *treeWalker) fail(reason TreeReason, rel string) error {
	return &TreeBudgetError{Tree: w.p.Label, Reason: reason, Class: pathClass(rel)}
}

func (w *treeWalker) openChild(parent *os.File, name, rel string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	switch {
	case err == nil:
		return os.NewFile(uintptr(fd), "tree"), nil
	case errors.Is(err, unix.ELOOP) || errors.Is(err, unix.ENOTDIR):
		return nil, w.fail(TreeSymlink, rel)
	default:
		return nil, w.fail(TreeWalk, rel)
	}
}

func (w *treeWalker) dir(f *os.File, rel string, depth int) error {
	for {
		batch, rerr := f.ReadDir(treeBatch)
		if w.p.afterList != nil && len(batch) > 0 {
			w.p.afterList(rel)
		}
		for _, e := range batch {
			if err := w.entry(f, e.Name(), path.Join(rel, e.Name()), depth+1); err != nil {
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

func (w *treeWalker) entry(parent *os.File, name, rel string, depth int) error {
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
	var st unix.Stat_t
	if err := unix.Fstatat(int(parent.Fd()), name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		if errors.Is(err, unix.ENOENT) {
			return nil // removed (healed) by another walker or the owner
		}
		return w.fail(TreeWalk, rel)
	}
	switch st.Mode & unix.S_IFMT {
	case unix.S_IFLNK:
		if w.aliasAllowed(parent, name, rel) {
			return w.count(rel, 0)
		}
		return w.heal(parent, name, rel, TreeSymlink)
	case unix.S_IFDIR:
		child, err := w.openChild(parent, name, rel)
		if err != nil {
			return err
		}
		defer child.Close()
		return w.dir(child, rel, depth)
	case unix.S_IFREG:
		return w.count(rel, st.Size)
	default:
		return w.heal(parent, name, rel, TreeSpecialFile)
	}
}

// aliasAllowed accepts a symlink only when the policy names its exact path and
// its target is acceptable; the link is read, never followed.
func (w *treeWalker) aliasAllowed(parent *os.File, name, rel string) bool {
	if w.p.allowSymlink == nil {
		return false
	}
	buf := make([]byte, 1024)
	n, err := unix.Readlinkat(int(parent.Fd()), name, buf)
	if err != nil || n <= 0 || n >= len(buf) {
		return false
	}
	return w.p.allowSymlink(rel, string(buf[:n]))
}

// heal unlinks one planted symlink or special file through the verified parent
// directory handle: the final component is never followed and a directory
// swapped in meanwhile makes the step fail (refusing admission). With
// checkOnly (authorization predicate) nothing is mutated: the entry is
// tolerated and counted, to be removed by the next locked Start.
func (w *treeWalker) heal(parent *os.File, name, rel string, reason TreeReason) error {
	if w.p.checkOnly && w.p.heal {
		return w.count(rel, 0)
	}
	if !w.p.heal {
		return w.fail(reason, rel)
	}
	if w.p.beforeHeal != nil {
		w.p.beforeHeal(rel)
	}
	fd := int(parent.Fd())
	var st unix.Stat_t
	if err := unix.Fstatat(fd, name, &st, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return w.healResult(err, rel)
	}
	if st.Mode&unix.S_IFMT == unix.S_IFDIR {
		return w.fail(TreeHeal, rel)
	}
	if err := unix.Unlinkat(fd, name, 0); err != nil {
		return w.healResult(err, rel)
	}
	if w.p.onHeal != nil {
		w.p.onHeal(reason, pathClass(rel))
	}
	return nil
}

// healResult treats ENOENT as already healed by a concurrent walker.
func (w *treeWalker) healResult(err error, rel string) error {
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	return w.fail(TreeHeal, rel)
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
