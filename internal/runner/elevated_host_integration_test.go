//go:build integration

package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

// Explicit opt-in: grants host authority, uses a disposable DB and native homes,
// executes only fixed read-only probes, and never touches ordinary containers.
// Keep controller elevated activation off: the reserved name is host-global.
func TestElevatedHostLifecycleProof(t *testing.T) {
	if os.Getenv("GOPOD_ELEVATED_HOST_PROOF") != "1" {
		t.Skip("requires explicit host qualification authorization")
	}
	image := os.Getenv("GOPOD_ELEVATED_TEST_IMAGE")
	codexImage := os.Getenv("GOPOD_ELEVATED_TEST_CODEX_IMAGE")
	if image == "" || codexImage == "" {
		t.Fatal("requires both provider images")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	d, err := NewDocker(ctx, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err = d.ElevatedSupported(ctx); err != nil {
		t.Fatal(err)
	}
	ids, err := d.ElevatedIDs(ctx)
	if err != nil || len(ids) > 0 {
		t.Fatal("refusing existing elevated runtime", ids, err)
	}
	facts, err := d.InspectContainer(ctx, ContainerName(ElevatedFolder))
	if err != nil || facts != nil {
		t.Fatal("reserved name occupied", err)
	}
	root := t.TempDir()
	for _, name := range []string{"data", "chats"} {
		if err = os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	st, err := store.Open(ctx, filepath.Join(root, "state.sqlite"), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	paths := Paths{RepoRoot: root, DataDir: filepath.Join(root, "data"), ChatsDir: filepath.Join(root, "chats"), ContainerSkillsDir: root, EmptyFile: filepath.Join(root, "empty")}
	cfg := ElevatedSpawnConfig{Image: image, Version: "e6-qualification", MemoryBytes: 128 << 20, NanoCPUs: 500000000, PidsLimit: 64}
	manager := NewElevated(st, d, paths, cfg, codexImage)
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 20*time.Second)
		defer stop()
		if err := manager.Close(cleanup, "proof cleanup"); err != nil {
			t.Error(err)
		}
	}()
	if _, err = manager.Start(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	argv := []string{"sh", "-ec", `test "$(id -u)" = 0; for ns in pid net ipc; do test "$(readlink /proc/self/ns/$ns)" = "$(readlink /proc/1/ns/$ns)"; done; test -r /host/etc/os-release; nsenter -t 1 -m -p -n -- true; test "$(cat /sys/fs/cgroup/memory.max)" = 134217728; echo host-proof-ok`}
	result, err := runElevatedJournaled(ctx, st, manager.session.ID, argv, func() (*ExecResult, error) { return d.ExecElevated(ctx, manager.container, argv) })
	if err != nil || result == nil || result.ExitCode != 0 || result.Stdout != "host-proof-ok\n" {
		t.Fatal(result, err)
	}
	rows, err := st.ListElevatedExecs(ctx, 1)
	if err != nil || len(rows) != 1 || !rows[0].EndedAt.Valid {
		t.Fatal(rows, err)
	}
	if err = manager.Close(ctx, "explicit close proof"); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Start(ctx, "codex"); err != nil {
		t.Fatal(err)
	}
	result, err = runElevatedJournaled(ctx, st, manager.session.ID, argv, func() (*ExecResult, error) { return d.ExecElevated(ctx, manager.container, argv) })
	if err != nil || result == nil || result.ExitCode != 0 || result.Stdout != "host-proof-ok\n" {
		t.Fatal("codex image host probe", result, err)
	}
	survivor := manager.container
	recovered := NewElevated(st, d, paths, cfg, codexImage)
	if err = recovered.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if recovered.Active() || !d.containerDead(ctx, survivor) {
		t.Fatal("boot survivor not removed")
	}
	if _, active, err := st.ActiveElevatedSession(ctx); err != nil || active {
		t.Fatal("durable live state remains", err)
	}
	t.Log("host namespaces, nsenter, memory cap, journal settlement, explicit close and fresh-manager survivor removal passed")
}
