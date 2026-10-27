package groupruntime

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"github.com/Coriol-is/gopod/internal/isolationproxy"
	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/docker/docker/client"
)

// Fail after network creation, before any container/native execution. This
// exercises Start's actual deferred cleanup, not reconciliation's other path.
func TestStartNetworkRemovalFailureBlocksFurtherAdmission(t *testing.T) {
	for _, failRemove := range []bool{false, true} {
		t.Run(map[bool]string{false: "removed", true: "unknown"}[failRemove], func(t *testing.T) {
			m, _ := migrationManager(t)
			c, err := m.Store.GetRegistered(t.Context(), "tg:-2")
			if err != nil {
				t.Fatal(err)
			}
			epoch, err := m.Store.RotateGroupEpoch(t.Context(), c.JID, c.RegistrationRevision)
			if err != nil {
				t.Fatal(err)
			}
			paths, err := runner.PrepareNamespacePaths(m.DataDir, epoch.CurrentScope, m.UID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = m.Store.CompleteGroupEpoch(t.Context(), epoch.CurrentScope); err != nil {
				t.Fatal(err)
			}
			c, err = m.Store.GetRegistered(t.Context(), c.JID)
			if err != nil {
				t.Fatal(err)
			}
			auth := filepath.Join(m.DataDir, "synthetic-auth.json")
			if err = os.WriteFile(auth, []byte(`{"synthetic":"not-a-credential"}`), 0600); err != nil {
				t.Fatal(err)
			}
			m.Config = Config{Qualification: "synthetic-qualified", EnabledGroups: []string{c.JID}, RelayImage: "relay-image", ToolImage: "tool-image", Providers: map[string]ProviderConfig{"claude": {Image: "group-image", AuthFile: auth, Routes: []isolationproxy.Route{{Host: "api.anthropic.com", Method: "POST", Path: "/v1/messages", Body: isolationproxy.AnthropicMessages}}}}}
			m.MCP = &groupsandbox.MCP{Executor: &groupsandbox.Executor{Root: filepath.Join(m.DataDir, "snapshots"), Image: "tool-image", UID: m.UID, GID: m.GID}}
			if err = os.Mkdir(m.MCP.Executor.Root, 0700); err != nil {
				t.Fatal(err)
			}
			var removed atomic.Int32
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == "GET" && (strings.HasSuffix(r.URL.Path, "/containers/json") || strings.HasSuffix(r.URL.Path, "/networks")):
					w.Write([]byte(`[]`))
				case r.Method == "GET" && strings.Contains(r.URL.Path, "/images/"):
					id := "personal-image"
					labels := map[string]string{}
					if strings.Contains(r.URL.Path, "/images/group-image/") {
						id = "group-image"
						labels = map[string]string{"org.gopod.profile": "group", "org.gopod.provider": "claude", "org.gopod.native-policy": runner.GroupNativePolicyVersion}
					}
					if strings.Contains(r.URL.Path, "/images/tool-image/") {
						id = "tool-image"
						labels = map[string]string{"org.gopod.profile": "group-tools", "org.gopod.native-policy": revision}
					}
					if strings.Contains(r.URL.Path, "/images/relay-image/") {
						id = "relay-image"
					}
					json.NewEncoder(w).Encode(map[string]any{"Id": id, "Config": map[string]any{"Labels": labels}})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/networks/create"):
					json.NewEncoder(w).Encode(map[string]string{"Id": "created-network"})
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/networks/created-network"):
					// Invalid internal topology must reject before starting a runtime.
					w.Write([]byte(`{"Id":"created-network","Internal":false}`))
				case r.Method == "DELETE" && strings.HasSuffix(r.URL.Path, "/networks/created-network"):
					removed.Add(1)
					if failRemove {
						w.WriteHeader(500)
						w.Write([]byte(`{"message":"synthetic upstream token=must-never-escape"}`))
					} else {
						w.WriteHeader(204)
					}
				default:
					t.Errorf("unexpected Docker operation %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithHTTPClient(srv.Client()), client.WithVersion("1.47"))
			if err != nil {
				t.Fatal(err)
			}
			defer cli.Close()
			m.Docker = cli
			if err = m.Authorize(t.Context(), c); err != nil {
				t.Fatalf("positive admission: %v", err)
			}
			_, _, release, err := m.Start(t.Context(), c, runner.NewClaudeProvider(""), 0, "")
			release()
			var admission *AdmissionError
			if !errors.As(err, &admission) || admission.Stage != "network" || !errors.Is(err, ErrUnavailable) {
				t.Fatalf("unexpected structural error: %v", err)
			}
			if strings.Contains(err.Error(), "must-never-escape") {
				t.Fatal("upstream error exposed")
			}
			if removed.Load() != 1 {
				t.Fatalf("network cleanup count %d", removed.Load())
			}
			mark, err := m.Store.GetState(t.Context(), "group_cleanup_unknown:"+c.NamespaceID)
			if err != nil {
				t.Fatal(err)
			}
			if failRemove {
				if mark != "1" || m.Authorize(t.Context(), c) == nil {
					t.Fatal("unknown network cleanup failed to block admission")
				}
			} else if mark != "" || m.Authorize(t.Context(), c) != nil {
				t.Fatal("successful cleanup blocked admission")
			}
			if _, err = os.Stat(paths.Native); err != nil {
				t.Fatal("native namespace removed")
			}
		})
	}
}
