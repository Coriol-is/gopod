package ipc

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func TestProtectedIPCUsesCurrentRegistrationRootsAndPreservesRetiredData(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(t.Context(), filepath.Join(base, "store.sqlite"), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	for _, c := range []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:-2", Folder: "old-group", AddedAt: 2}} {
		if err = st.RegisterChat(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	group, _ := st.GetRegistered(t.Context(), "tg:-2")
	migration, err := st.RotateGroupEpoch(t.Context(), group.JID, group.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := runner.PrepareNamespacePaths(base, migration.CurrentScope, runner.NamespaceHostUID())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = st.CompleteGroupEpoch(t.Context(), migration.CurrentScope); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	msgs := &fakeMessageSink{}
	denyGroup, inventoryFailed := false, false
	cfg := Config{DataDir: base, OwnerFolder: "owner", Namespaces: func(ctx context.Context) ([]string, error) {
		if inventoryFailed {
			return nil, errors.New("unavailable")
		}
		rows, e := st.ListRegistered(ctx)
		if e != nil {
			return nil, e
		}
		var out []string
		for _, c := range rows {
			out = append(out, c.Folder)
		}
		return out, nil
	}, NamespacePath: func(folder string) (string, bool) {
		c, e := st.GetByFolder(t.Context(), folder)
		if e != nil {
			return "", false
		}
		if c.IsOwner {
			return filepath.Join(base, "ipc", c.Folder), true
		}
		if denyGroup || !c.GroupEpochReady {
			return "", false
		}
		p, e := runner.ValidateNamespacePaths(base, c.Scope(), runner.NamespaceHostUID())
		return p.IPC, e == nil
	}}
	w := New(cfg, msgs, &fakeTaskSink{}, slog.New(slog.NewTextHandler(&logs, nil)), func(folder string) (string, bool) {
		c, e := st.GetByFolder(t.Context(), folder)
		return c.JID, e == nil
	})
	writeIPC(t, base, "owner", "messages", "owner.json", map[string]string{"chatJid": "tg:1", "text": "owner-positive"})
	writeIPC(t, base, "old-group", "messages", "retired.json", map[string]string{"chatJid": "tg:-2", "text": "retired-private-canary"})
	group, _ = st.GetRegistered(t.Context(), "tg:-2")
	// A malicious legacy alias at the current storage key cannot become fallback.
	writeIPC(t, base, group.Folder, "messages", "legacy.json", map[string]string{"chatJid": "tg:-2", "text": "legacy-private-canary"})
	raw, _ := json.Marshal(map[string]string{"chatJid": "tg:-2", "text": "group-positive"})
	if err = os.WriteFile(filepath.Join(paths.IPC, "messages", "group.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.tickOnce(t.Context())
	if got := msgs.callsSnapshot(); len(got) != 2 {
		t.Fatalf("positive controls: %#v", got)
	}
	for _, path := range []string{filepath.Join(paths.IPC, ".processed", "group.json"), filepath.Join(paths.IPC, "results", "messages", "group.result.json"), filepath.Join(base, "ipc", "owner", ".processed", "owner.json"), filepath.Join(base, "ipc", "old-group", "messages", "retired.json"), filepath.Join(base, "ipc", group.Folder, "messages", "legacy.json")} {
		if _, err = os.Stat(path); err != nil {
			t.Fatalf("required preserved/routed file %s: %v", path, err)
		}
	}
	denyGroup = true
	if err = os.WriteFile(filepath.Join(paths.IPC, "messages", "denied.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	w.tickOnce(t.Context())
	if len(msgs.callsSnapshot()) != 2 {
		t.Fatal("false resolver fell back")
	}
	if _, err = os.Stat(filepath.Join(paths.IPC, "messages", "denied.json")); err != nil {
		t.Fatal("denied request consumed")
	}
	denyGroup = false
	inventoryFailed = true
	w.tickOnce(t.Context())
	if len(msgs.callsSnapshot()) != 2 {
		t.Fatal("inventory failure dispatched")
	}
	inventoryFailed = false
	outside := t.TempDir()
	privateName := "OUTSIDE-PRIVATE-FILENAME-CANARY.json"
	if err = os.WriteFile(filepath.Join(outside, privateName), []byte(`{"op":"list"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(filepath.Join(paths.IPC, "tasks"), filepath.Join(paths.IPC, "tasks-saved")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, filepath.Join(paths.IPC, "tasks")); err != nil {
		t.Fatal(err)
	}
	writeIPC(t, base, "owner", "messages", "owner2.json", map[string]string{"chatJid": "tg:1", "text": "healthy-after-attack"})
	w.tickOnce(t.Context())
	if len(msgs.callsSnapshot()) != 3 {
		t.Fatal("unsafe group blocked healthy owner")
	}
	if strings.Contains(logs.String(), privateName) {
		t.Fatal("outside basename leaked")
	}
	if _, err = os.Stat(filepath.Join(outside, privateName)); err != nil {
		t.Fatal("outside file modified")
	}
}
