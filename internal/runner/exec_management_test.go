package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
)

type managementTestExec struct {
	*fakeAgentExec
	run func(context.Context, []string) (*ExecResult, error)
}

func (e *managementTestExec) Exec(ctx context.Context, _ string, cmd, _ []string) (*ExecResult, error) {
	return e.run(ctx, cmd)
}

func TestManagementDockerDuplexAndCleanup(t *testing.T) {
	for _, stall := range []bool{false, true} {
		t.Run(fmt.Sprint("stall=", stall), func(t *testing.T) {
			var running, killed atomic.Bool
			var command []string
			serverDone := make(chan struct{})
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
				switch strings.TrimPrefix(req.URL.Path, "/v1.47") {
				case "/containers/existing/exec":
					var options container.ExecOptions
					if err := json.NewDecoder(req.Body).Decode(&options); err != nil {
						t.Error(err)
					}
					command = options.Cmd
					if !options.AttachStdin || options.Tty || !options.AttachStdout || !options.AttachStderr {
						t.Error("invalid stdio options")
					}
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprint(w, `{"Id":"probe"}`)
				case "/exec/probe/start":
					_, _ = io.Copy(io.Discard, req.Body)
					conn, rw, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					defer close(serverDone)
					fmt.Fprint(rw, "HTTP/1.1 101 UPGRADED\r\nContent-Type: application/vnd.docker.raw-stream\r\nConnection: Upgrade\r\nUpgrade: tcp\r\n\r\n")
					_ = rw.Flush()
					running.Store(true)
					out := stdcopy.NewStdWriter(conn, stdcopy.Stdout)
					scanner := bufio.NewScanner(rw)
					for scanner.Scan() {
						if len(scanner.Bytes()) == 0 {
							continue
						}
						var m struct {
							ID     int    `json:"id"`
							Method string `json:"method"`
						}
						if json.Unmarshal(scanner.Bytes(), &m) != nil {
							t.Error("invalid JSON")
						}
						if stall {
							continue
						}
						switch m.Method {
						case "initialize":
							fmt.Fprintln(out, `{"id":1,"result":{}}`)
						case "initialized":
						case "account/read":
							fmt.Fprintln(out, `{"id":2,"result":{"account":{"type":"apiKey"}}}`)
						default:
							t.Errorf("unsafe method %s", m.Method)
						}
					}
				case "/exec/probe/json":
					w.Header().Set("Content-Type", "application/json")
					fmt.Fprintf(w, `{"ID":"probe","Running":%t,"ExitCode":0}`, running.Load())
				default:
					t.Errorf("unexpected endpoint %s", req.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer srv.Close()
			cli, err := client.NewClientWithOpts(client.WithHost("tcp://"+srv.Listener.Addr().String()), client.WithVersion("1.47"), client.WithHTTPClient(srv.Client()))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			r := &Runner{d: &Docker{cli: cli, log: quietLog()}, log: quietLog()}
			r.ex = &managementTestExec{fakeAgentExec: newFakeAgentExec(), run: func(ctx context.Context, cmd []string) (*ExecResult, error) {
				if len(cmd) != 4 || cmd[0] != "sh" || !strings.Contains(cmd[2], "kill -TERM") || !strings.HasPrefix(cmd[3], "/tmp/gopod-agent-") {
					t.Error("cleanup not scoped pid-group")
				}
				killed.Store(true)
				running.Store(false)
				return &ExecResult{}, nil
			}}
			ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
			defer cancel()
			report, err := r.probeCodexManagement(ctx, "existing")
			if stall && err == nil {
				t.Fatal("stalled transport returned success")
			}
			if !stall && (err != nil || report.Quota != "not_applicable") {
				t.Fatalf("report=%+v err=%v", report, err)
			}
			if !killed.Load() || running.Load() {
				t.Fatal("appserver left running")
			}
			if len(command) < 8 || strings.Join(command[4:], " ") != "codex app-server --listen stdio://" {
				t.Fatalf("command=%v", command)
			}
			select {
			case <-serverDone:
			case <-time.After(time.Second):
				t.Fatal("attach not closed")
			}
		})
	}
}
