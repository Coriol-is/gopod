package ipc

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGroupReceiptsUseSafeErrorsButPersonalDiagnosticsStayUnchanged(t *testing.T) {
	root := t.TempDir()
	secret := "private auth-file /protected/auth.json upstream http://gopod-relay:8787/mcp/" + strings.Repeat("a", 64)
	w := New(Config{DataDir: root, OwnerFolder: "owner"}, nil, nil, nil, staticResolver(map[string]string{"group": "tg:-2", "owner": "tg:1"}))
	for _, folder := range []string{"group", "owner"} {
		for _, channel := range []string{"tasks", "messages"} {
			writeIPC(t, root, folder, channel, "request.json", map[string]string{"op": "list"})
			source := filepath.Join(root, "ipc", folder, channel, "request.json")
			relative := "results/request.result.json"
			ok := false
			if channel == "messages" {
				ok = w.writeMessageReceipt(folder, source, "error", errors.New(secret))
				relative = "results/messages/request.result.json"
			} else {
				ok = w.writeTaskReceipt(folder, source, "list", nil, errors.New(secret))
			}
			if !ok {
				t.Fatal("receipt not written")
			}
			raw, err := os.ReadFile(filepath.Join(root, "ipc", folder, relative))
			if err != nil {
				t.Fatal(err)
			}
			var receipt struct {
				Status string `json:"status"`
				Error  string `json:"error"`
			}
			if json.Unmarshal(raw, &receipt) != nil {
				t.Fatal("invalid receipt")
			}
			if receipt.Status != "error" {
				t.Fatal("delivery/task status lost")
			}
			if folder == "group" {
				if receipt.Error != "operation_unavailable" || strings.Contains(string(raw), "/protected/") {
					t.Fatal("group received raw error")
				}
			} else if receipt.Error != secret {
				t.Fatal("personal diagnostics changed")
			}
		}
	}
}

func TestUnresolvedReceiptSourceCannotRecoverRawDiagnostics(t *testing.T) {
	w := New(Config{OwnerFolder: "owner"}, nil, nil, nil, func(string) (string, bool) { return "", false })
	private := errors.New("private native error http://group:" + strings.Repeat("a", 64) + "@gopod-relay:8787")
	if w.receiptError("group", private) != "operation_unavailable" {
		t.Fatal("failed resolver exposed shared diagnostics")
	}
	if w.receiptError("owner", private) != private.Error() {
		t.Fatal("known owner private diagnostics changed")
	}
}
