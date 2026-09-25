package runner

import (
	"io"
	"strings"
	"testing"
	"time"
)

// TestReadExecStreamStderrBeforeStdout reproduces the codex hang:
// ExecStream demuxes stdout and stderr from a single goroutine into two
// unbuffered pipes, so a reader that leaves stderr unread until stdout
// hits EOF deadlocks as soon as the agent writes to stderr first.
func TestReadExecStreamStderrBeforeStdout(t *testing.T) {
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()

	// Single writer, like stdcopy.StdCopy: stderr progress, then the answer.
	go func() {
		defer stdoutW.Close()
		defer stderrW.Close()
		stderrW.Write([]byte("codex: thinking...\n"))
		stdoutW.Write([]byte("hello "))
		stderrW.Write([]byte("codex: tokens used 42\n"))
		stdoutW.Write([]byte("world"))
	}()

	var chunks []string
	type out struct{ full, stderr string }
	done := make(chan out, 1)
	go func() {
		full, stderr := readExecStream(stdoutR, stderrR, func(s string) { chunks = append(chunks, s) })
		done <- out{full, stderr}
	}()

	select {
	case got := <-done:
		if got.full != "hello world" {
			t.Errorf("full = %q, want %q", got.full, "hello world")
		}
		if !strings.Contains(got.stderr, "thinking") || !strings.Contains(got.stderr, "tokens used") {
			t.Errorf("stderr = %q, want both progress lines", got.stderr)
		}
		if strings.Join(chunks, "") != "hello world" {
			t.Errorf("chunks = %q, want them to add up to %q", chunks, "hello world")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("readExecStream deadlocked: stderr written before stdout EOF was never drained")
	}
}
