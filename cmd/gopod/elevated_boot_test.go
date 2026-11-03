package main

import (
	"bufio"
	"context"
	"flag"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type bootTransport func(*http.Request) (*http.Response, error)

func (f bootTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// Exercise real run() wiring with both features enabled. No Docker daemon,
// credentials, network, production store or Telegram account is accessed.
func TestElevatedControllerBoot(t *testing.T) {
	if os.Getenv("GOPOD_BOOT_FIXTURE") == "1" {
		http.DefaultTransport = bootTransport(func(r *http.Request) (*http.Response, error) {
			result := `true`
			switch {
			case strings.HasSuffix(r.URL.Path, "getMe"):
				result = `{"id":123,"is_bot":true,"first_name":"fixture","username":"fixture_bot"}`
			case strings.HasSuffix(r.URL.Path, "getUpdates"):
				select {
				case <-r.Context().Done():
					return nil, r.Context().Err()
				case <-time.After(20 * time.Millisecond):
				}
				result = `[]`
			case strings.HasSuffix(r.URL.Path, "sendMessage"):
				result = `{"message_id":1,"chat":{"id":123,"type":"private"},"date":1}`
			}
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"ok":true,"result":` + result + `}`)), Request: r}, nil
		})
		flag.CommandLine = flag.NewFlagSet("boot-fixture", flag.ContinueOnError)
		os.Args = []string{os.Args[0], "-dotenv=false"}
		if err := run(); err != nil {
			t.Fatal(err)
		}
		return
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestElevatedControllerBoot$")
	root := t.TempDir()
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "GOPOD_BOOT_FIXTURE=1", "GOPOD_ELEVATED=1", "GOPOD_OWNER_CHAT_ID=123", "TELEGRAM_BOT_TOKEN=123:synthetic-test-only", "GOPOD_DATA_DIR=" + filepath.Join(root, "data"), "GOPOD_REPO_ROOT=" + root, "DOCKER_HOST=unix://" + filepath.Join(root, "missing.sock")}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	cmd.Stdout = w
	cmd.Stderr = w
	if err = cmd.Start(); err != nil {
		_ = w.Close()
		t.Fatal(err)
	}
	_ = w.Close()
	ready := make(chan struct{}, 1)
	output := make(chan string, 1)
	go func() {
		var b strings.Builder
		scan := bufio.NewScanner(r)
		for scan.Scan() {
			line := scan.Text()
			b.WriteString(line + "\n")
			if strings.Contains(line, "gopod ready") {
				select {
				case ready <- struct{}{}:
				default:
				}
			}
		}
		output <- b.String()
	}()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-ready:
		_ = cmd.Process.Signal(os.Interrupt)
		if err = <-exited; err != nil {
			t.Fatal("shutdown failed", err, <-output)
		}
		<-output
	case text := <-output:
		t.Fatal("controller failed before readiness", text)
	case <-ctx.Done():
		t.Fatal("controller readiness timed out")
	}
}
