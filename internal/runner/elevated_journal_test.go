package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type journalProbe struct {
	events              []string
	beginErr, finishErr error
	settledCanceled     bool
}

func (p *journalProbe) BeginElevatedExec(context.Context, string, []string) (int64, error) {
	p.events = append(p.events, "intent")
	return 1, p.beginErr
}
func (p *journalProbe) FinishElevatedExec(ctx context.Context, _ int64, _ int) error {
	p.events = append(p.events, "settled")
	p.settledCanceled = ctx.Err() != nil
	return p.finishErr
}

func TestElevatedJournalOrdersAndFailsClosed(t *testing.T) {
	for _, which := range []string{"success", "intent_failure", "settlement_failure", "cancel"} {
		t.Run(which, func(t *testing.T) {
			p := &journalProbe{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if which == "intent_failure" {
				p.beginErr = errors.New("disk unavailable")
			}
			if which == "settlement_failure" {
				p.finishErr = errors.New("disk unavailable")
			}
			result, err := runElevatedJournaled(ctx, p, "session", []string{"tool"}, func() (*ExecResult, error) {
				p.events = append(p.events, "exec")
				if which == "cancel" {
					cancel()
				}
				return &ExecResult{ExitCode: 0}, nil
			})
			if p.beginErr != nil {
				if len(p.events) != 1 || result != nil || err == nil {
					t.Fatal("exec occurred without intent")
				}
				return
			}
			if len(p.events) != 3 || p.events[0] != "intent" || p.events[1] != "exec" || p.events[2] != "settled" {
				t.Fatal(p.events)
			}
			if p.finishErr != nil {
				if result != nil || err == nil {
					t.Fatal("published unsettled result")
				}
			} else if err != nil || result == nil {
				t.Fatal(err)
			}
			if p.settledCanceled {
				t.Fatal("settlement used cancelled context")
			}
		})
	}
}

func TestElevatedBootstrapOnlyOwnDirectories(t *testing.T) {
	p := mkPaths(t)
	workspace, claude, codex, err := EnsureElevatedDirs(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{workspace, claude, codex} {
		info, err := os.Stat(d)
		if err != nil || !info.IsDir() {
			t.Fatalf("missing directory %s: %v", d, err)
		}
	}
	if _, err := os.Stat(filepath.Join(p.DataDir, "ipc", ElevatedFolder)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("unexpected IPC namespace")
	}
	if _, _, _, err := EnsureElevatedDirs(p); err != nil {
		t.Fatal(err)
	}
}

func TestElevatedBootstrapRejectsSymlink(t *testing.T) {
	p := mkPaths(t)
	if err := os.Symlink(t.TempDir(), filepath.Join(p.ChatsDir, ElevatedFolder)); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := EnsureElevatedDirs(p); err == nil {
		t.Fatal("accepted symlink")
	}
}

func TestElevatedIdleCleanupIsExcluded(t *testing.T) {
	// A nil Docker facade would panic if the idle path attempted any operation.
	r := silentRunner()
	r.killIdleChat(context.Background(), ElevatedFolder)
}

func TestElevatedFolderRefusedByOrdinaryBuilders(t *testing.T) {
	c := goodSpawnConfig()
	c.ChatFolder = ElevatedFolder
	if _, _, _, err := BuildContainerArgs(c); err == nil {
		t.Fatal("ordinary spawn accepted reserved name")
	}
	p := mkPaths(t)
	if _, err := BuildMounts(p, ElevatedFolder, TierOwner, nil); err == nil {
		t.Fatal("ordinary mounts accepted reserved name")
	}
	if err := EnsureChatDirs(p, ElevatedFolder, true, nil); err == nil {
		t.Fatal("ordinary bootstrap accepted reserved name")
	}
}

func TestElevatedBootstrapRefusesLegacyState(t *testing.T) {
	p := mkPaths(t)
	dir := filepath.Join(p.DataDir, "sessions", ElevatedFolder)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "old-auth"), []byte("private"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := EnsureElevatedDirs(p); err == nil {
		t.Fatal("inherited legacy native state")
	}
}
