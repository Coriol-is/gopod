package ipc

import (
	"encoding/json"
	"fmt"
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
		receipt.Error = cause.Error()
	}
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return false
	}
	root, err := os.OpenRoot(filepath.Join(w.cfg.DataDir, "ipc", folder))
	if err != nil {
		w.log.Error("ipc: open receipt namespace", "folder", folder, "err", err)
		return false
	}
	defer root.Close()
	if err = root.MkdirAll("results", 0o700); err != nil {
		w.log.Error("ipc: create result directory", "folder", folder, "err", err)
		return false
	}
	name := strings.TrimSuffix(filepath.Base(path), ".json") + ".result.json"
	tmp := filepath.Join("results", ".tmp-"+name+"-"+strconv.FormatInt(w.cfg.NowFn().UnixNano(), 10))
	f, err := root.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err == nil {
		_, err = f.Write(append(data, '\n'))
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
	}
	if err == nil {
		err = root.Rename(tmp, filepath.Join("results", name))
	}
	if err != nil {
		root.Remove(tmp)
		w.log.Error("ipc: write task receipt", "folder", folder, "file", filepath.Base(path), "err", fmt.Errorf("receipt: %w", err))
		return false
	}
	return true
}
