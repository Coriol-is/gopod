//go:build integration

package runner

import (
	"bufio"
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
)

func TestElevatedExecSupervisorDocker(t *testing.T) {
	image := os.Getenv("GOPOD_ELEVATED_TEST_IMAGE")
	if image == "" {
		t.Skip("set GOPOD_ELEVATED_TEST_IMAGE to a built elevated image")
	}
	d, err := NewDocker(t.Context(), quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.CreateStart(t.Context(), "gopod-elevated-supervisor-test", &container.Config{Image: image, Cmd: []string{"sleep", "infinity"}}, &container.HostConfig{NetworkMode: "none"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = d.Remove(ctx, id)
	})
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	r, err := d.ExecElevated(ctx, id, []string{"sh", "-c", "printf supervisor-ok; exit 7"})
	if err != nil || r.ExitCode != 7 || r.Stdout != "supervisor-ok" {
		t.Fatal(r, err)
	}
	input, writer := io.Pipe()
	var loginOutput bytes.Buffer
	go func() { _, _ = io.WriteString(writer, "synthetic-code\n") }()
	loginResult, loginErr := d.ExecElevatedLogin(ctx, id, []string{"sh", "-c", "read code; test \"$code\" = synthetic-code; printf login-ok"}, input, &loginOutput)
	_ = writer.Close()
	if loginErr != nil || loginResult == nil || loginResult.ExitCode != 0 || loginOutput.String() != "login-ok" || loginResult.Stdout != "" || loginResult.Stderr != "" {
		t.Fatal("interactive stdin or output isolation failed", loginResult, loginErr)
	}
	stopCtx, stop := context.WithCancel(t.Context())
	defer stop()
	done := make(chan *ExecResult, 1)
	errCh := make(chan error, 1)
	go func() { r, e := d.ExecElevated(stopCtx, id, []string{"sh", "-c", "sleep 60"}); done <- r; errCh <- e }()
	// Wait for a real child, rather than cancelling before exec attachment.
	deadline := time.Now().Add(5 * time.Second)
	for {
		top, e := d.cli.ContainerTop(ctx, id, []string{"-eo", "pid,args"})
		if e != nil {
			t.Fatal(e)
		}
		found := false
		for _, row := range top.Processes {
			if strings.Contains(strings.Join(row, " "), "sleep 60") {
				found = true
			}
		}
		if found {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child not started")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stop()
	select {
	case r = <-done:
	case <-ctx.Done():
		t.Fatal("cancellation did not settle")
	}
	err = <-errCh
	if err != nil || r == nil || r.ExitCode != 137 {
		t.Fatal(r, err)
	}
	if idle, e := d.quiescent(ctx, id); e != nil || !idle {
		t.Fatal("child survived", e)
	}
	r, err = d.ExecElevated(ctx, id, []string{"echo", "container-still-live"})
	if err != nil || !strings.Contains(r.Stdout, "container-still-live") {
		t.Fatal(r, err)
	}
}

// Manual network qualification against the pinned native CLI, with a disposable
// home and no credentials/host mounts. Never completes provider authorization.
func TestElevatedLoginNativePrompt(t *testing.T) {
	providerName := os.Getenv("GOPOD_ELEVATED_LOGIN_PROVIDER")
	image := os.Getenv("GOPOD_ELEVATED_TEST_IMAGE")
	if providerName == "" || image == "" {
		t.Skip("requires explicit provider prompt qualification")
	}
	var provider AgentProvider
	switch providerName {
	case "claude":
		provider = NewClaudeProvider(image)
	case "codex":
		provider = NewCodexProvider(image)
	default:
		t.Fatal("unsupported provider")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 40*time.Second)
	defer cancel()
	d, err := NewDocker(ctx, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	id, err := d.CreateStart(ctx, "gopod-elevated-login-prompt-"+providerName, &container.Config{Image: image, Cmd: []string{"sleep", "infinity"}}, &container.HostConfig{})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		cleanup, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		_ = d.Remove(cleanup, id)
	}()
	input, inputWriter := io.Pipe()
	defer inputWriter.Close()
	output, outputWriter := io.Pipe()
	defer output.Close()
	context.AfterFunc(ctx, func() { _ = output.Close() })
	done := make(chan struct{})
	var result *ExecResult
	var execErr error
	go func() {
		defer close(done)
		result, execErr = d.ExecElevatedLogin(ctx, id, provider.LoginCmd(), input, outputWriter)
		_ = outputWriter.Close()
	}()
	scanner := bufio.NewScanner(output)
	url, code := false, false
	for scanner.Scan() {
		parsed := provider.ExtractLoginURL(scanner.Text())
		if strings.HasPrefix(parsed, "CODE:") {
			code = true
		} else if strings.HasPrefix(parsed, "https://") {
			url = true
		}
		if url && (providerName == "claude" || code) {
			break
		}
	}
	cancel()
	_, _ = io.Copy(io.Discard, output)
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("prompt cancellation did not settle")
	}
	if !url || (providerName == "codex" && !code) {
		t.Fatal("native login prompt not parsed; no raw auth output retained")
	}
	if result == nil {
		t.Fatal("native login termination unproven", execErr)
	}
	t.Log("native login prompt parsed; cancelled without authorization")
}
