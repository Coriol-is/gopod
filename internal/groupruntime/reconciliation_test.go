package groupruntime

import (
	"context"
	"encoding/json"
	"github.com/Coriol-is/gopod/internal/groupsandbox"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
)

type cleanupDocker struct {
	mu           sync.Mutex
	containers   map[string]container.Summary
	networks     map[string]network.Summary
	failedRemove string
	stickyRemove string
	seen         []string
	removed      []string
}

func (d *cleanupDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	args, _ := filters.FromJSON(r.URL.Query().Get("filters"))
	matches := func(labels map[string]string) bool {
		for _, condition := range args.Get("label") {
			pair := strings.SplitN(condition, "=", 2)
			if len(pair) != 2 || labels[pair[0]] != pair[1] {
				return false
			}
		}
		return true
	}
	switch {
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/containers/json"):
		d.seen = append(d.seen, r.URL.Query().Get("filters"))
		found := []container.Summary{}
		for _, c := range d.containers {
			if matches(c.Labels) {
				found = append(found, c)
			}
		}
		json.NewEncoder(w).Encode(found)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/networks"):
		found := []network.Summary{}
		for _, n := range d.networks {
			if matches(n.Labels) {
				found = append(found, n)
			}
		}
		json.NewEncoder(w).Encode(found)
	case r.Method == "DELETE":
		id := filepath.Base(r.URL.Path)
		if id == d.failedRemove {
			w.WriteHeader(500)
			io.WriteString(w, `{"message":"synthetic unknown cleanup"}`)
			return
		}
		d.removed = append(d.removed, id)
		if id != d.stickyRemove {
			delete(d.containers, id)
			delete(d.networks, id)
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"unexpected Docker operation"}`)
	}
}
func reconciliationFixture(t *testing.T) (*Manager, store.ScopeRef, runner.NamespacePaths, *cleanupDocker) {
	t.Helper()
	m, _ := migrationManager(t)
	c, _ := m.Store.GetRegistered(t.Context(), "tg:-2")
	migration, err := m.Store.RotateGroupEpoch(t.Context(), c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := runner.PrepareNamespacePaths(m.DataDir, migration.CurrentScope, m.UID)
	if err != nil {
		t.Fatal(err)
	}
	f := &cleanupDocker{containers: map[string]container.Summary{}, networks: map[string]network.Summary{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	cli, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithHTTPClient(srv.Client()), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	m.Docker = cli
	scope := migration.CurrentScope
	f.containers["native"] = container.Summary{ID: "native", Labels: map[string]string{"gopod.group-native": scope.NamespaceID, runner.LabelChat: scope.Folder}}
	f.containers["relay"] = container.Summary{ID: "relay", Labels: map[string]string{"gopod.group-native": scope.NamespaceID}}
	f.containers["tool-retired"] = container.Summary{ID: "tool-retired", Labels: map[string]string{"gopod.group-tool": "old-group"}}
	f.containers["tool-current"] = container.Summary{ID: "tool-current", Labels: map[string]string{"gopod.group-tool": scope.Folder}}
	f.containers["personal"] = container.Summary{ID: "personal", Labels: map[string]string{runner.LabelChat: "owner"}}
	f.containers["foreign-tool"] = container.Summary{ID: "foreign-tool", Labels: map[string]string{"gopod.group-tool": "foreign"}}
	f.networks["group-net"] = network.Summary{ID: "group-net", Labels: map[string]string{"gopod.group-native": scope.NamespaceID}}
	f.networks["personal-net"] = network.Summary{ID: "personal-net"}
	for _, dir := range []string{paths.Native, paths.Workspace, paths.Memory, paths.IPC} {
		if err = os.WriteFile(filepath.Join(dir, "preserved-canary"), []byte("group-data"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err = os.Mkdir(filepath.Join(paths.Policy, "turn-orphan"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.Policy, "turn-orphan", "relay.json"), []byte("synthetic ephemeral policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(paths.Policy, "keep-policy.json"), []byte("persistent policy"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(m.DataDir, "personal-auth-canary")
	if err = os.WriteFile(source, []byte("synthetic not a credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Link(source, filepath.Join(paths.Policy, "auth-ref-orphan")); err != nil {
		t.Fatal(err)
	}
	return m, scope, paths, f
}
func TestReconcileSweepsAllReservedToolsAndOnlyEphemeralPolicy(t *testing.T) {
	m, scope, paths, f := reconciliationFixture(t)
	if err := m.reconcile(t.Context(), scope.NamespaceID); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(paths.Policy, "turn-orphan"), filepath.Join(paths.Policy, "auth-ref-orphan")} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("orphan policy retained")
		}
	}
	for _, path := range []string{filepath.Join(paths.Policy, "keep-policy.json"), filepath.Join(m.DataDir, "personal-auth-canary"), filepath.Join(paths.Native, "preserved-canary"), filepath.Join(paths.Workspace, "preserved-canary"), filepath.Join(paths.Memory, "preserved-canary"), filepath.Join(paths.IPC, "preserved-canary")} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved state removed: %s", path)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.containers) != 2 || f.containers["personal"].ID != "personal" || f.containers["foreign-tool"].ID != "foreign-tool" || len(f.networks) != 1 {
		t.Fatal("foreign/personal Docker resources changed")
	}
	sort.Strings(f.removed)
	if got := strings.Join(f.removed, ","); got != "group-net,native,relay,tool-current,tool-retired" {
		t.Fatalf("cleanup set %s", got)
	}
	if !strings.Contains(strings.Join(f.seen, " "), "gopod.group-tool=old-group") {
		t.Fatal("retired tool scope not reconciled")
	}
}
func TestReconcileUnknownRemovalOrSettlementKeepsPolicyAndBlocks(t *testing.T) {
	for _, failure := range []string{"tool-retired", "group-net", "sticky-native"} {
		t.Run(failure, func(t *testing.T) {
			m, scope, paths, f := reconciliationFixture(t)
			if failure == "sticky-native" {
				f.stickyRemove = "native"
			} else {
				f.failedRemove = failure
			}
			if err := m.reconcile(t.Context(), scope.NamespaceID); err == nil {
				t.Fatal("unsettled cleanup accepted")
			}
			flag, _ := m.Store.GetState(t.Context(), "group_cleanup_unknown:"+scope.NamespaceID)
			if flag != "1" {
				t.Fatal("unknown cleanup not recorded")
			}
			for _, path := range []string{filepath.Join(paths.Policy, "turn-orphan", "relay.json"), filepath.Join(paths.Policy, "auth-ref-orphan"), filepath.Join(m.DataDir, "personal-auth-canary"), filepath.Join(paths.Native, "preserved-canary")} {
				if _, err := os.Stat(path); err != nil {
					t.Fatal("unverified cleanup discarded state")
				}
			}
		})
	}
}
func TestReconcileNeverFollowsPolicySymlink(t *testing.T) {
	m, scope, paths, _ := reconciliationFixture(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "private-canary")
	if err := os.WriteFile(target, []byte("do not touch"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(paths.Policy, "turn-outside")); err != nil {
		t.Fatal(err)
	}
	if err := m.reconcile(t.Context(), scope.NamespaceID); err == nil {
		t.Fatal("unsafe policy entry accepted")
	}
	if raw, err := os.ReadFile(target); err != nil || string(raw) != "do not touch" {
		t.Fatal("outside state changed")
	}
	if _, err := os.Stat(filepath.Join(paths.Policy, "turn-orphan", "relay.json")); err != nil {
		t.Fatal("unsafe GC partly discarded forensic files")
	}
}
func TestRequireInactiveIncludesRetiredToolScopes(t *testing.T) {
	m, scope, _, f := reconciliationFixture(t)
	f.containers = map[string]container.Summary{"orphan": {ID: "orphan", Labels: map[string]string{"gopod.group-tool": "old-group"}}}
	f.networks = map[string]network.Summary{}
	if err := m.requireInactiveNamespace(t.Context(), scope); err == nil {
		t.Fatal("retired tool orphan ignored")
	}
	if len(f.removed) != 0 {
		t.Fatal("inactive guard killed a process")
	}
}

func TestExplicitEnableRepairCannotForgetTimedOutHostExport(t *testing.T) {
	m, scope, paths, f := reconciliationFixture(t)
	m.MCP = &groupsandbox.MCP{}
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	token, _, err := m.MCP.Grant(t.Context(), groupsandbox.Lease{Scope: scope.Folder, Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }, Request: func(context.Context, json.RawMessage) (string, error) {
		close(entered)
		<-release
		return "stalled host export", nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(finished)
		m.MCP.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_request","arguments":{}}}`)))
	}()
	<-entered
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Millisecond)
	if err = m.MCP.RevokeAndWait(ctx, token); err == nil {
		t.Fatal("stalled callback accepted")
	}
	cancel()
	ctx, cancel = context.WithTimeout(t.Context(), 250*time.Millisecond)
	if _, err = m.Enable(ctx, scope.JID); err == nil {
		t.Fatal("enable repaired before host export settled")
	}
	cancel()
	f.mu.Lock()
	removed := len(f.removed)
	f.mu.Unlock()
	if removed != 0 {
		t.Fatal("Docker/policy cleanup started before host settlement")
	}
	if _, err = os.Stat(filepath.Join(paths.Policy, "turn-orphan", "relay.json")); err != nil {
		t.Fatal("unsettled host work lost policy evidence")
	}
	marker, _ := m.Store.GetState(t.Context(), "group_cleanup_unknown:"+scope.NamespaceID)
	if marker != "1" {
		t.Fatal("repair lost cleanup-unknown marker")
	}
	close(release)
	<-finished
	if err = m.reconcile(t.Context(), scope.NamespaceID); err != nil {
		t.Fatal("settled host work could not repair", err)
	}
	if _, err = os.Stat(filepath.Join(paths.Policy, "turn-orphan")); !os.IsNotExist(err) {
		t.Fatal("settled repair did not GC known temps")
	}
	current, _ := m.Store.GetRegistered(t.Context(), scope.JID)
	if current.GroupEpochReady {
		t.Fatal("reconciliation silently enabled group")
	}
}

func TestPrepareDoesNotRotateEpochWithUnsettledHostWork(t *testing.T) {
	m, scope, _, f := reconciliationFixture(t)
	ctx := t.Context()
	m.MCP = &groupsandbox.MCP{}
	// No Docker resource remains, but a revoked host callback is still in flight.
	f.containers = map[string]container.Summary{}
	f.networks = map[string]network.Summary{}
	if _, err := m.Store.CompleteGroupEpoch(ctx, scope); err != nil {
		t.Fatal(err)
	}
	current, _ := m.Store.GetRegistered(ctx, scope.JID)
	entered, release, finished := make(chan struct{}), make(chan struct{}), make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()
	token, _, err := m.MCP.Grant(ctx, groupsandbox.Lease{Scope: current.Folder, Expires: time.Now().Add(time.Minute), Validate: func(context.Context) error { return nil }, Request: func(context.Context, json.RawMessage) (string, error) { close(entered); <-release; return "late", nil }})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(finished)
		m.MCP.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/mcp/"+token, strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"group_request","arguments":{}}}`)))
	}()
	<-entered
	short, cancel := context.WithTimeout(ctx, 15*time.Millisecond)
	if err = m.MCP.RevokeAndWait(short, token); err == nil {
		t.Fatal("stalled callback settled")
	}
	cancel()
	short, cancel = context.WithTimeout(ctx, 250*time.Millisecond)
	if _, err = m.Prepare(short, current.JID); err == nil {
		t.Fatal("prepare rotated before host work settled")
	}
	cancel()
	after, _ := m.Store.GetRegistered(ctx, current.JID)
	if after.Scope() != current.Scope() {
		t.Fatal("old epoch retired while host work remained")
	}
	disabled, _ := m.Store.GetState(ctx, "group_disabled:"+current.NamespaceID)
	if disabled != "1" {
		t.Fatal("failed prepare left admission open")
	}
	close(release)
	<-finished
	if _, err = m.Prepare(ctx, current.JID); err != nil {
		t.Fatal("settled preparation failed", err)
	}
	after, _ = m.Store.GetRegistered(ctx, current.JID)
	if after.NamespaceEpoch != current.NamespaceEpoch+1 || after.GroupEpochReady {
		t.Fatal("settled preparation did not create disabled clean epoch")
	}
}
