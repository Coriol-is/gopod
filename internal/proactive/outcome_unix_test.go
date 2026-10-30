//go:build unix

package proactive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// readWithTimeout fails the test if ReadOutcome blocks: a hostile agent may
// leave a FIFO where the outcome file should be.
func readWithTimeout(t *testing.T, dir, id string, beforeOpen func()) (Outcome, error) {
	t.Helper()
	type res struct {
		o   Outcome
		err error
	}
	ch := make(chan res, 1)
	go func() {
		o, err := readOutcome(dir, id, beforeOpen)
		ch <- res{o, err}
	}()
	select {
	case r := <-ch:
		return r.o, r.err
	case <-time.After(3 * time.Second):
		t.Fatal("ReadOutcome blocked")
		return Outcome{}, nil
	}
}

func TestReadOutcomeDoesNotBlockOnAFIFO(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, "proactive")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Mkfifo(filepath.Join(pd, "gmail.urgent.last"), 0o644); err != nil {
		t.Skipf("mkfifo: %v", err)
	}
	if _, err := readWithTimeout(t, dir, "gmail.urgent", nil); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a FIFO must be refused, got %v", err)
	}
}

// The file is swapped for a FIFO after the type check and before the open.
func TestReadOutcomeSurvivesAFIFOSwappedInAfterTheCheck(t *testing.T) {
	dir, path := outcomeDir(t, `{"result":"sent"}`)
	swap := func() {
		_ = os.Remove(path)
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Errorf("mkfifo: %v", err)
		}
	}
	_, err := readWithTimeout(t, dir, "gmail.urgent", swap)
	if err == nil || !strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("err = %v, want a not-a-regular-file refusal", err)
	}
}

// A symlink that stays inside the workspace is swapped in after the check.
func TestReadOutcomeRefusesAnInWorkspaceSymlinkSwappedInAfterTheCheck(t *testing.T) {
	dir, path := outcomeDir(t, `{"result":"empty"}`)
	if err := os.WriteFile(filepath.Join(dir, "elsewhere"), []byte(`{"result":"sent","note":"planted"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	swap := func() {
		_ = os.Remove(path)
		if err := os.Symlink("../elsewhere", path); err != nil {
			t.Errorf("symlink: %v", err)
		}
	}
	if o, err := readWithTimeout(t, dir, "gmail.urgent", swap); err == nil {
		t.Fatalf("read through a swapped symlink: %+v", o)
	}
}

func TestReadOutcomeRefusesAHardLink(t *testing.T) {
	dir, path := outcomeDir(t, `{"result":"sent"}`)
	if err := os.Link(path, filepath.Join(dir, "other-name")); err != nil {
		t.Skipf("link: %v", err)
	}
	_, err := readWithTimeout(t, dir, "gmail.urgent", nil)
	if err == nil || !strings.Contains(err.Error(), "hard link") {
		t.Fatalf("err = %v, want a hard-link refusal", err)
	}
}

func outcomeDir(t *testing.T, body string) (dir, path string) {
	t.Helper()
	dir = t.TempDir()
	pd := filepath.Join(dir, "proactive")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	path = filepath.Join(pd, "gmail.urgent.last")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir, path
}
