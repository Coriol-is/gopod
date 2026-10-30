//go:build unix

package proactive

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// readWithTimeout fails the test if ReadOutcome blocks: a hostile agent may
// leave a FIFO where the outcome file should be.
func readWithTimeout(t *testing.T, dir, id string) (Outcome, error) {
	t.Helper()
	type res struct {
		o   Outcome
		err error
	}
	ch := make(chan res, 1)
	go func() {
		o, err := ReadOutcome(dir, id)
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
	if _, err := readWithTimeout(t, dir, "gmail.urgent"); err == nil || errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a FIFO must be refused, got %v", err)
	}
}

// The file is swapped for a FIFO after the type check and before the open.
func TestReadOutcomeSurvivesAFIFOSwappedInAfterTheCheck(t *testing.T) {
	dir := t.TempDir()
	pd := filepath.Join(dir, "proactive")
	if err := os.MkdirAll(pd, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(pd, "gmail.urgent.last")
	if err := os.WriteFile(path, []byte(`{"result":"sent"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	beforeOpenHook = func() {
		_ = os.Remove(path)
		if err := syscall.Mkfifo(path, 0o644); err != nil {
			t.Errorf("mkfifo: %v", err)
		}
	}
	t.Cleanup(func() { beforeOpenHook = nil })
	if _, err := readWithTimeout(t, dir, "gmail.urgent"); err == nil {
		t.Fatal("a swapped-in FIFO must be refused")
	}
}
