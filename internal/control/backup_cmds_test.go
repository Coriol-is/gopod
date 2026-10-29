package control

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/backup"
	"github.com/Coriol-is/gopod/internal/maintenance"
	"github.com/Coriol-is/gopod/internal/store"
)

type liveContainers struct{}

func (liveContainers) LiveOwned(context.Context) ([]string, error) {
	return []string{"deadbeefdeadbeef"}, nil
}

func backupRouter(t *testing.T) (*Router, BackupDeps) {
	t.Helper()
	base := t.TempDir()
	d := BackupDeps{
		DataDir: filepath.Join(base, "data"), StorePath: filepath.Join(base, "data", "store.sqlite"),
		ArtifactsDir: filepath.Join(base, "data", "artifacts"), DefaultRoot: filepath.Join(base, "bk"), Version: "t",
	}
	st, err := store.Open(context.Background(), d.StorePath, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.SetState(context.Background(), "k", "v"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r := New(nil)
	RegisterBackup(r, d)
	return r, d
}

func cli(args ...string) Command {
	return Command{Name: args[0], Args: args[1:], Caller: Caller{Source: SourceCLI, IsOwner: true}}
}

func TestBackupCommandsAreOwnerOnlyAndNotReachableFromChat(t *testing.T) {
	r, _ := backupRouter(t)
	for _, name := range []string{"backup.create", "backup.verify", "backup.list", "backup.idle", "backup.quarantine"} {
		_, err := r.Dispatch(context.Background(), Command{Name: name, Caller: Caller{Source: SourceTelegram, ChatID: 5}})
		if !errors.Is(err, ErrNotAuthorized) {
			t.Errorf("%s non-owner: %v", name, err)
		}
		// Even an owner chat cannot reach it by slash text: no slash form.
		for _, info := range r.List() {
			if info.Name == name && info.SlashName != "" {
				t.Errorf("%s has slash form %q", name, info.SlashName)
			}
		}
	}
	if name, ok := r.LookupBySlash(""); ok {
		t.Fatalf("empty slash resolved to %q", name)
	}
}

func TestBackupCreateVerifyListIdleThroughRouter(t *testing.T) {
	ctx := context.Background()
	r, d := backupRouter(t)
	resp, err := r.Dispatch(ctx, cli("backup.create"))
	if err != nil || resp.Code != 0 || !strings.Contains(resp.Text, "not covered") {
		t.Fatalf("create: %v %+v", err, resp)
	}
	id := strings.Fields(strings.Split(resp.Text, "\n")[0])[2]
	resp, _ = r.Dispatch(ctx, cli("backup.list"))
	if !strings.Contains(resp.Text, id) || !strings.Contains(resp.Text, "sealed") {
		t.Fatalf("list: %q", resp.Text)
	}
	resp, _ = r.Dispatch(ctx, cli("backup.verify", id))
	if resp.Code != 0 || !strings.Contains(resp.Text, ": OK") {
		t.Fatalf("verify: %+v", resp)
	}
	resp, _ = r.Dispatch(ctx, cli("backup.idle"))
	if resp.Code != 0 || !strings.HasPrefix(resp.Text, "idle") {
		t.Fatalf("idle: %+v", resp)
	}
	// Tamper: verify reports a problem and a non-zero code.
	if err := os.WriteFile(filepath.Join(d.DefaultRoot, id, backup.DBName), []byte("tampered"), 0o600); err != nil {
		t.Fatal(err)
	}
	resp, _ = r.Dispatch(ctx, cli("backup.verify", id))
	if resp.Code == 0 || !strings.Contains(resp.Text, "NOT restorable") {
		t.Fatalf("tampered verify: %+v", resp)
	}
}

func TestBackupMatchedRefusesLiveContainersWithoutStoppingAnything(t *testing.T) {
	ctx := context.Background()
	r, d := backupRouter(t)
	r = New(nil)
	d.Containers = func(context.Context) (maintenance.ContainerLister, func(), error) {
		return liveContainers{}, func() {}, nil
	}
	RegisterBackup(r, d)
	resp, err := r.Dispatch(ctx, cli("backup.create", "--mode", "matched_stopped"))
	if err != nil || resp.Code != 3 || !strings.Contains(resp.Text, "nothing was stopped") {
		t.Fatalf("matched with live container: %v %+v", err, resp)
	}
	resp, _ = r.Dispatch(ctx, cli("backup.create", "--mode", "matched_stopped", "--assert-no-containers"))
	if resp.Code != 0 {
		t.Fatalf("asserted matched: %+v", resp)
	}
	resp, _ = r.Dispatch(ctx, cli("backup.list"))
	if strings.Count(resp.Text, "matched_stopped") != 1 {
		t.Fatalf("expected exactly one matched set: %q", resp.Text)
	}
}

func TestBackupQuarantineReleaseNeedsConfirmation(t *testing.T) {
	ctx := context.Background()
	r, d := backupRouter(t)
	if resp, _ := r.Dispatch(ctx, cli("backup.quarantine", "set")); resp.Code != 0 {
		t.Fatalf("set: %+v", resp)
	}
	if active, _ := maintenance.LatchActive(d.DataDir); !active {
		t.Fatal("latch not set")
	}
	if resp, _ := r.Dispatch(ctx, cli("backup.quarantine", "release")); resp.Code == 0 {
		t.Fatal("release without --yes succeeded")
	}
	c := cli("backup.quarantine", "release")
	c.Confirmed = true
	if resp, _ := r.Dispatch(ctx, c); resp.Code != 0 {
		t.Fatalf("release: %+v", resp)
	}
	if active, _ := maintenance.LatchActive(d.DataDir); active {
		t.Fatal("latch still set")
	}
}

func TestBackupUsageErrors(t *testing.T) {
	r, _ := backupRouter(t)
	for _, c := range []Command{cli("backup.verify"), cli("backup.create", "--bogus"), cli("backup.quarantine", "x"), cli("backup.idle", "extra")} {
		resp, err := r.Dispatch(context.Background(), c)
		if err != nil || resp.Code == 0 {
			t.Errorf("%v: %v %+v", c.Args, err, resp)
		}
	}
}
