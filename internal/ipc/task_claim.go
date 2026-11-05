package ipc

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
	"golang.org/x/sys/unix"
)

// The spool is outside every ordinary IPC mount. Only the singleton controller
// owns it. Never prune an unresolved claim or trust an agent-writable receipt.
const maxTaskClaimBytes = 4 << 20

var errClaimShape = errors.New("regular claim file required")
var errClaimOversize = errors.New("claim file exceeds bound")

type taskClaimRecord struct {
	Scope                                           store.ScopeRef
	Scoped                                          bool
	Version                                         int
	Folder, File, Namespace, NamespaceID, SourceJID string
	Owner                                           bool
	Snapshot                                        []byte
	Snapshotted                                     bool
	ReadError                                       string
	Receipt                                         *taskReceipt
	Identity                                        *TaskRequestIdentity
	Completed                                       bool
	Attempts                                        int
	RetryAt                                         int64
}

type taskClaim struct {
	dir       *os.File
	namespace *os.Root
	slot      string
	hook      func(string) error
	record    taskClaimRecord
}

func (c *taskClaim) close()     { c.dir.Close(); c.namespace.Close() }
func claimHash(s string) string { return fmt.Sprintf("%x", sha256.Sum256([]byte(s))) }

func (w *Watcher) taskClaimDir(folder string) (*os.File, error) {
	root, err := os.OpenRoot(w.cfg.DataDir)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	// Validate each private ancestor, not just the leaf. Agent namespaces cannot
	// install symlinks here or make a public directory into our recovery authority.
	for _, path := range []string{"task-claims", "task-claims/" + claimHash(folder)} {
		d, err := openDirectory(root, path, true)
		if err != nil {
			return nil, err
		}
		var st unix.Stat_t
		err = unix.Fstat(int(d.Fd()), &st)
		if err == nil && (st.Uid != uint32(os.Geteuid()) || st.Mode&0077 != 0) {
			err = fmt.Errorf("unsafe task claim directory")
		}
		if err == nil {
			err = d.Sync()
		}
		d.Close()
		if err != nil {
			return nil, err
		}
	}
	top, err := openDirectory(root, "task-claims", false)
	if err != nil {
		return nil, err
	}
	err = top.Sync()
	top.Close()
	if err != nil {
		return nil, err
	}
	parent, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	err = parent.Sync()
	parent.Close()
	if err != nil {
		return nil, err
	}
	return openDirectory(root, "task-claims/"+claimHash(folder), false)
}

func readClaimFile(dir *os.File, name string, limit int64) ([]byte, error) {
	fd, err := unix.Openat(int(dir.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errClaimShape
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err == nil && int64(len(b)) > limit {
		return b, errClaimOversize
	}
	return b, err
}

// atomicClaimWrite syncs both contents and the directory entry. The fixed temp
// name is safe in a host-only directory under the singleton controller lock.
func atomicClaimWrite(dir *os.File, name string, data []byte) error {
	tmp := ".tmp-" + name
	if err := unix.Unlinkat(int(dir.Fd()), tmp, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), tmp)
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name)
	}
	if err == nil {
		err = dir.Sync()
	}
	return err
}
func (c *taskClaim) save() error {
	raw, err := json.Marshal(c.record)
	if err != nil {
		return err
	}
	if len(raw) > maxTaskClaimBytes {
		return fmt.Errorf("task claim record exceeds bound")
	}
	return atomicClaimWrite(c.dir, c.slot+".json", raw)
}
func namespaceIdentity(root *os.Root) (string, error) {
	f, err := root.Open(".")
	if err != nil {
		return "", err
	}
	defer f.Close()
	var st unix.Stat_t
	if err = unix.Fstat(int(f.Fd()), &st); err != nil {
		return "", err
	}
	return fmt.Sprintf("%d:%d", st.Dev, st.Ino), nil
}

func (w *Watcher) openTaskClaim(ctx context.Context, folder, name string, create bool) (*taskClaim, error) {
	if filepath.Base(name) != name || name == "." || name == ".." || !strings.HasSuffix(name, ".json") {
		return nil, fmt.Errorf("invalid task filename")
	}
	var scope store.ScopeRef
	scoped := w.cfg.NamespaceScope != nil
	if scoped {
		var err error
		scope, err = w.cfg.NamespaceScope(ctx, folder)
		if err != nil {
			return nil, err
		}
	}
	ns, err := w.namespacePath(folder)
	if err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(ns)
	if err != nil {
		return nil, err
	}
	nsID, err := namespaceIdentity(root)
	if err != nil {
		root.Close()
		return nil, err
	}
	if w.resolve == nil {
		root.Close()
		return nil, fmt.Errorf("task resolver unavailable")
	}
	jid, ok := w.resolve(folder)
	if !ok || (scoped && (scope.Folder != folder || scope.JID != jid)) {
		root.Close()
		return nil, fmt.Errorf("task source unavailable")
	}
	dir, err := w.taskClaimDir(folder)
	if err != nil {
		root.Close()
		return nil, err
	}
	c := &taskClaim{dir: dir, namespace: root, slot: claimHash(name), hook: w.taskClaimHook}
	raw, err := readClaimFile(dir, c.slot+".json", maxTaskClaimBytes)
	if errors.Is(err, os.ErrNotExist) && create {
		entries, e := dir.ReadDir(maxDirectoryEntries)
		if e != nil && !errors.Is(e, io.EOF) {
			c.close()
			return nil, e
		}
		count := 0
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".json") {
				count++
			}
		}
		limit := min(max(w.cfg.Retention, 1), 200)
		if count >= limit {
			c.close()
			return nil, fmt.Errorf("task claim backlog full")
		}
		c.record = taskClaimRecord{Scope: scope, Scoped: scoped, Version: 1, Folder: folder, File: name, Namespace: ns, NamespaceID: nsID, SourceJID: jid, Owner: folder == w.cfg.OwnerFolder}
		err = c.save()
		if err == nil {
			err = c.checkpoint("prepared")
		}
	} else if err == nil {
		err = json.Unmarshal(raw, &c.record)
	}
	if err != nil {
		c.close()
		return nil, err
	}
	r := &c.record
	if r.Scoped != scoped || r.Scope != scope || r.Version != 1 || r.Folder != folder || r.File != name || r.Namespace != ns || r.NamespaceID != nsID || r.SourceJID != jid || r.Owner != (folder == w.cfg.OwnerFolder) {
		c.close()
		return nil, fmt.Errorf("task claim binding changed; held")
	}
	return c, nil
}

func (c *taskClaim) snapshot() error {
	if c.record.Snapshotted {
		return c.releaseInput()
	}
	src, err := openDirectory(c.namespace, "tasks", false)
	if err != nil {
		return err
	}
	defer src.Close()
	input := c.slot + ".input"
	var st unix.Stat_t
	err = unix.Fstatat(int(c.dir.Fd()), input, &st, unix.AT_SYMLINK_NOFOLLOW)
	if errors.Is(err, unix.ENOENT) {
		// No copy/delete fallback: EXDEV leaves the inbox entry unconsumed.
		if err = unix.Renameat(int(src.Fd()), c.record.File, int(c.dir.Fd()), input); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	if err = c.checkpoint("claimed"); err != nil {
		return err
	}
	if err = c.dir.Sync(); err != nil {
		return err
	}
	if err = src.Sync(); err != nil {
		return err
	}
	if err := c.checkpoint("read"); err != nil {
		return err
	}
	raw, err := readClaimFile(c.dir, input, 64<<10)
	switch {
	case errors.Is(err, errClaimOversize):
		c.record.ReadError = "task request exceeds 64 KiB"
	case errors.Is(err, errClaimShape), errors.Is(err, unix.ELOOP):
		c.record.ReadError = "cannot read task request: publish a regular non-symlink JSON file atomically inside its IPC namespace"
	case err != nil:
		return err // I/O/permissions/resource failure is a hold, never data loss.
	}
	c.record.Snapshot = raw
	c.record.Snapshotted = true
	if err := c.save(); err != nil {
		return err
	}
	if err := c.releaseInput(); err != nil {
		return err
	}
	return c.checkpoint("snapshot")
}

func (w *Watcher) recoverTaskClaims(ctx context.Context, folder string) map[string]bool {
	seen := make(map[string]bool)
	dir, err := w.taskClaimDir(folder)
	if err != nil {
		return seen
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxDirectoryEntries)
	if err != nil && !errors.Is(err, io.EOF) {
		return seen
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	var records []taskClaimRecord
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		raw, err := readClaimFile(dir, e.Name(), maxTaskClaimBytes)
		if err != nil {
			continue
		}
		var rec taskClaimRecord
		if json.Unmarshal(raw, &rec) != nil || claimHash(rec.File)+".json" != e.Name() {
			continue
		}
		seen[rec.File] = true
		records = append(records, rec)
	}
	if len(records) > 0 {
		start := int(w.claimSweep % uint64(len(records)))
		for i := 0; i < min(len(records), maxChannelDispatches/2) && ctx.Err() == nil; i++ {
			rec := records[(start+i)%len(records)]
			w.safeDispatch(ctx, folder, filepath.Join(rec.Namespace, "tasks", rec.File), w.processTaskFile)
		}
	}

	return seen
}

func (w *Watcher) finishTaskClaim(ctx context.Context, c *taskClaim) bool {
	r := &c.record
	if err := w.validateTaskClaim(ctx, c); err != nil {
		w.log.Warn("ipc: stale task completion held", "err", err)
		return false
	}
	if r.Receipt == nil {
		return false
	}
	if !r.Completed {
		dest := ".processed"
		if r.Receipt.Status != "ok" {
			dest = ".failed"
		}
		archive, err := openDirectory(c.namespace, dest, true)
		if err != nil {
			return false
		}
		err = atomicClaimWrite(archive, r.File, r.Snapshot)
		archive.Close()
		if err != nil {
			return false
		}
		results, err := openDirectory(c.namespace, "results", true)
		if err != nil {
			return false
		}
		raw, err := json.MarshalIndent(r.Receipt, "", "  ")
		if err == nil {
			err = atomicClaimWrite(results, strings.TrimSuffix(r.File, ".json")+".result.json", append(raw, '\n'))
		}
		results.Close()
		if err != nil {
			return false
		}
		parent, e := c.namespace.Open(".")
		if e != nil {
			return false
		}
		e = parent.Sync()
		parent.Close()
		if e != nil {
			return false
		}
		if err = c.checkpoint("receipt"); err != nil {
			return false
		}
		r.Completed = true
		if err = c.save(); err != nil {
			return false
		}
	}
	if err := c.checkpoint("terminal"); err != nil {
		return false
	}
	if r.Identity != nil {
		processor, ok := w.tasks.(TaskRequestProcessor)
		if !ok {
			return false
		}
		if err := processor.AcknowledgeTaskRequest(ctx, *r.Identity); err != nil {
			return false
		}
	}
	if err := c.checkpoint("acknowledged"); err != nil {
		return false
	}
	// Remove mutable claimed inode first. A surviving terminal record never
	// dispatches again, even if journal retention has since removed its row.
	if err := unix.Unlinkat(int(c.dir.Fd()), c.slot+".input", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return false
	}
	if err := c.dir.Sync(); err != nil {
		return false
	}
	if err := unix.Unlinkat(int(c.dir.Fd()), c.slot+".json", 0); err != nil {
		return false
	}
	return c.dir.Sync() == nil
}

func (w *Watcher) completeTaskClaim(ctx context.Context, c *taskClaim, op string, result *TaskResult, cause error, id *TaskRequestIdentity) bool {
	c.record.Receipt = &taskReceipt{Status: "ok", RequestFile: c.record.File, Op: op, Result: result, Help: "Read /workspace/chat/SCHEDULING.md; use op and camelCase fields, then inspect this result before claiming success."}
	// Hash only a complete bounded snapshot; never label a truncated prefix or
	// unreadable inode as the submitted request. Invalid JSON still has exact bytes.
	if c.record.Snapshotted && c.record.ReadError == "" {
		c.record.Receipt.RequestHash = fmt.Sprintf("%x", sha256.Sum256(c.record.Snapshot))
	}
	if cause != nil {
		c.record.Receipt.Status = "error"
		c.record.Receipt.Error = w.receiptError(c.record.Folder, cause)
	}
	c.record.Identity = id
	if err := c.save(); err != nil {
		w.log.Warn("ipc: save task outcome", "err", err)
		return false
	}
	if err := c.checkpoint("outcome"); err != nil {
		return false
	}
	return w.finishTaskClaim(ctx, c)
}

func (w *Watcher) deferTaskClaim(c *taskClaim) bool {
	c.record.Attempts++
	if c.record.Attempts >= w.cfg.MaxRetries {
		return true
	}
	i := min(c.record.Attempts-1, len(w.cfg.RetryBackoff)-1)
	c.record.RetryAt = w.cfg.NowFn().Add(w.cfg.RetryBackoff[i]).UnixNano()
	if err := c.save(); err != nil {
		w.log.Warn("ipc: save task retry", "err", err)
	}
	return false
}

func (c *taskClaim) retryDue(now time.Time) bool { return c.record.RetryAt <= now.UnixNano() }

func (c *taskClaim) checkpoint(stage string) error {
	if c.hook != nil {
		return c.hook(stage)
	}
	return nil
}

func (w *Watcher) validateTaskClaim(ctx context.Context, c *taskClaim) error {
	r := c.record
	if w.cfg.NamespaceAllowed != nil && !w.cfg.NamespaceAllowed(ctx, r.Folder) {
		return fmt.Errorf("task namespace denied")
	}
	ns, err := w.namespacePath(r.Folder)
	if err != nil || ns != r.Namespace {
		return fmt.Errorf("task namespace changed")
	}
	root, err := os.OpenRoot(ns)
	if err != nil {
		return err
	}
	defer root.Close()
	id, err := namespaceIdentity(root)
	if err != nil || id != r.NamespaceID {
		return fmt.Errorf("task namespace inode changed")
	}
	jid, ok := w.resolve(r.Folder)
	if !ok || jid != r.SourceJID || r.Owner != (r.Folder == w.cfg.OwnerFolder) {
		return fmt.Errorf("task registration changed")
	}
	if r.Scoped {
		scope, err := w.cfg.NamespaceScope(ctx, r.Folder)
		if err != nil || scope != r.Scope {
			return fmt.Errorf("task namespace scope changed")
		}
	}
	return nil
}

// Once the snapshot is durable, retain no agent-owned inode that an old open
// descriptor could continue growing. Pre-snapshot claims remain preserved on I/O
// failure and are not covered by the JSON-record byte bound.
func (c *taskClaim) releaseInput() error {
	if err := unix.Unlinkat(int(c.dir.Fd()), c.slot+".input", 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return err
	}
	return c.dir.Sync()
}
