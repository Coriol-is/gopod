package ipc

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Coriol-is/gopod/internal/store"
)

// durableReceipt is the diagnostic receipt of a durably admitted request.
// Status is one of queued, sent, uncertain, failed (delivery progress),
// retrying (admission deferred) or error (request rejected, never sent).
// Queued means committed to the outbox, not delivered.
type durableReceipt struct {
	Status        string `json:"status"`
	RequestFile   string `json:"requestFile"`
	DeliveryState string `json:"deliveryState,omitempty"`
	Detail        string `json:"detail,omitempty"`
	Duplicate     bool   `json:"duplicate,omitempty"`
	Error         string `json:"error,omitempty"`
	Help          string `json:"help"`
}

const durableHelp = "Read /workspace/chat/SCHEDULING.md. status queued means committed to the host outbox, not delivered; " +
	"it becomes sent, uncertain or failed. Never resend after uncertain: the message may already have arrived."

// durableReceiptFor maps a delivery group state to the agent-visible status.
func durableReceiptFor(state, detail string) durableReceipt {
	status := "queued"
	switch state {
	case store.DeliverySent:
		status = "sent"
	case store.DeliveryUncertain:
		status = "uncertain"
	case store.DeliveryFailed, store.DeliveryExpired, store.DeliveryCancelled:
		status = "failed"
	}
	return durableReceipt{Status: status, DeliveryState: state, Detail: detail}
}

func receiptBaseName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json") + ".result.json"
}

func conflictReceiptName(path string) string {
	return strings.TrimSuffix(filepath.Base(path), ".json") + ".conflict.result.json"
}

func (w *Watcher) writeDurableReceipt(folder, path string, rc durableReceipt) bool {
	return w.writeDurableReceiptAs(folder, receiptBaseName(path), withRequest(rc, path))
}

func withRequest(rc durableReceipt, path string) durableReceipt {
	if rc.RequestFile == "" {
		rc.RequestFile = filepath.Base(path)
	}
	return rc
}

func (w *Watcher) writeDurableReceiptAs(folder, name string, rc durableReceipt) bool {
	if rc.Help == "" {
		rc.Help = durableHelp
	}
	return w.writeReceiptNamed(folder, name, "results/messages", rc)
}

// rememberReceipt records the delivery state last written for a request so the
// reconcile sweep rewrites a receipt only when the state changed.
func (w *Watcher) rememberReceipt(folder, key, state string) {
	if w.receiptStates == nil {
		w.receiptStates = make(map[string]string)
	}
	w.receiptStates[folder+"\x00"+key] = state
}

// readReceiptState returns the deliveryState of an existing receipt, or "".
func (w *Watcher) readReceiptState(folder, name string) string {
	root, err := w.openNamespace(folder)
	if err != nil {
		return ""
	}
	defer root.Close()
	f, err := root.OpenFile(filepath.Join("results/messages", name), os.O_RDONLY|syscall.O_NONBLOCK|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ""
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return ""
	}
	data, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return ""
	}
	var rc durableReceipt
	if json.Unmarshal(data, &rc) != nil {
		return ""
	}
	return rc.DeliveryState
}

// reconcileDurableReceipts recreates and advances receipts from durable state:
// after a crash between commit and receipt, and as deliveries progress from
// queued to sent, uncertain or failed. It only reads the store and writes
// receipts; it never publishes, resends or archives.
func (w *Watcher) reconcileDurableReceipts(ctx context.Context, folder string) {
	d := w.cfg.Durable
	if d.Store == nil || !w.durableActor(folder) {
		return
	}
	src, err := d.Store.SnapshotScope(ctx, folder)
	if err != nil {
		return
	}
	rows, err := d.Store.RecentIPCRequests(ctx, src, w.cfg.Retention)
	if err != nil {
		w.log.Warn("ipc: list durable requests", "folder", folder, "err", err)
		return
	}
	for _, r := range rows {
		key := folder + "\x00" + r.Key
		name := strings.TrimSuffix(r.Key, ".json") + ".result.json"
		known, cached := w.receiptStates[key]
		if !cached {
			known = w.readReceiptState(folder, name)
		}
		if known == r.DeliveryState {
			w.rememberReceipt(folder, r.Key, r.DeliveryState)
			continue
		}
		detail := ""
		if diag, err := d.Store.DeliveryDiagnostic(ctx, r.DeliveryID); err == nil {
			detail = diag.Summary()
		}
		rc := durableReceiptFor(r.DeliveryState, detail)
		rc.RequestFile = r.Key
		if w.writeDurableReceiptAs(folder, name, rc) {
			w.rememberReceipt(folder, r.Key, r.DeliveryState)
		}
	}
}
