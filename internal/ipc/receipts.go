package ipc

import (
	"encoding/json"
	"fmt"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type taskReceipt struct {
	Status      string      `json:"status"`
	RequestFile string      `json:"requestFile"`
	Op          string      `json:"op,omitempty"`
	Result      *TaskResult `json:"result,omitempty"`
	Error       string      `json:"error,omitempty"`
	Help        string      `json:"help"`
}

// A Root capability prevents an agent-created results symlink from escaping
// its own IPC namespace and turning a receipt into a host arbitrary-file write.
func (w *Watcher) writeTaskReceipt(folder, path, op string, result *TaskResult, cause error) bool {
	receipt := taskReceipt{Status: "ok", RequestFile: filepath.Base(path), Op: op, Result: result, Help: "Read /workspace/chat/SCHEDULING.md; use op and camelCase fields, then inspect this result before claiming success."}
	if cause != nil {
		receipt.Status = "error"
		receipt.Error = w.receiptError(folder, cause)
	}
	return w.writeReceipt(folder, path, "results", receipt)
}

func (w *Watcher) writeReceipt(folder, path, directory string, receipt any) bool {
	name := strings.TrimSuffix(filepath.Base(path), ".json") + ".result.json"
	return w.writeReceiptNamed(folder, name, directory, receipt)
}

// writeReceiptNamed atomically publishes a receipt under an explicit file name.
func (w *Watcher) writeReceiptNamed(folder, name, directory string, receipt any) bool {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return false
	}
	root, err := w.openNamespace(folder)
	if err != nil {
		w.log.Error("ipc: open receipt namespace", "folder", folder, "err", err)
		return false
	}
	defer root.Close()
	if filepath.Base(name) != name || name == "." || name == ".." {
		return false
	}
	dir, err := openDirectory(root, directory, true)
	if err != nil {
		w.log.Error("ipc: unsafe result directory", "folder", folder, "err", err)
		return false
	}
	defer dir.Close()
	tmp := ".tmp-" + name + "-" + strconv.FormatInt(w.cfg.NowFn().UnixNano(), 10)
	fd, err := unix.Openat(int(dir.Fd()), tmp, unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0600)
	if err == nil {
		f := os.NewFile(uintptr(fd), tmp)
		_, err = f.Write(append(data, '\n'))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = unix.Renameat(int(dir.Fd()), tmp, int(dir.Fd()), name)
	}
	if err != nil {
		unix.Unlinkat(int(dir.Fd()), tmp, 0)
		w.log.Error("ipc: write receipt", "folder", folder, "file", name, "err", fmt.Errorf("receipt: %w", err))
		return false
	}
	return true
}

// Message receipts are diagnostic only, not delivery replay authority. Telegram
// has no atomic transaction with our filesystem; resubmission may duplicate.
func (w *Watcher) writeMessageReceipt(folder, path, status string, cause error) bool {
	receipt := struct {
		Status      string `json:"status"`
		RequestFile string `json:"requestFile"`
		Error       string `json:"error,omitempty"`
		Help        string `json:"help"`
	}{Status: status, RequestFile: filepath.Base(path), Help: "Read /workspace/chat/SCHEDULING.md. Message receipts are under results/messages; use canonical chatJid tg:<id>. Do not blindly resend after an ambiguous delivery error."}
	if cause != nil {
		receipt.Error = w.receiptError(folder, cause)
	}
	return w.writeReceipt(folder, path, "results/messages", receipt)
}

// Group-visible receipts are a disclosure boundary, not an owner diagnostic
// channel. Unknown sink/storage errors may contain sensitive URLs or paths.
func (w *Watcher) receiptError(folder string, cause error) string {
	if cause == nil {
		return ""
	}
	if w.resolve != nil {
		jid, ok := w.resolve(folder)
		if ok {
			id, err := strconv.ParseInt(strings.TrimPrefix(jid, "tg:"), 10, 64)
			if err == nil && id < 0 {
				return "operation_unavailable"
			}
			if err == nil && id > 0 {
				return cause.Error()
			}
		}
	}
	// A failed late lookup cannot turn a shared/unknown source into a private
	// diagnostic channel. Only the already host-selected owner alias is retained.
	if folder != "" && folder == w.cfg.OwnerFolder {
		return cause.Error()
	}
	return "operation_unavailable"
}
