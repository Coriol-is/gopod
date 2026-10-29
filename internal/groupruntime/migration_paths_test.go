package groupruntime

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/client"
)

type migrationDocker struct {
	mu        sync.Mutex
	legacy    bool
	stopped   bool // an exited legacy container exists until it is removed
	removed   []string
	seen      []string
	mutations int
}

func (d *migrationDocker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	defer d.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "DELETE" && d.stopped && strings.HasSuffix(r.URL.Path, "/containers/legacy-stopped") && r.URL.Query().Get("force") != "1" && r.URL.Query().Get("force") != "true" {
		d.stopped = false
		d.removed = append(d.removed, "legacy-stopped")
		w.WriteHeader(204)
		return
	}
	if r.Method != "GET" {
		d.mutations++
		w.WriteHeader(500)
		io.WriteString(w, `{"message":"unexpected Docker mutation"}`)
		return
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/containers/json"):
		filter := r.URL.Query().Get("filters")
		d.seen = append(d.seen, filter)
		if d.stopped && strings.Contains(filter, "gopod.chat=old-group") {
			io.WriteString(w, `[{"Id":"legacy-stopped","Names":["/old-group-agent"],"State":"exited","Labels":{"gopod.chat":"old-group"}}]`)
		} else if d.legacy && strings.Contains(filter, "gopod.chat=old-group") {
			io.WriteString(w, `[{"Id":"legacy-running","State":"running","Labels":{"gopod.chat":"old-group"}}]`)
		} else {
			io.WriteString(w, `[]`)
		}
	case strings.Contains(r.URL.Path, "/images/") && strings.HasSuffix(r.URL.Path, "/json"):
		json.NewEncoder(w).Encode(map[string]any{"Id": "sha256:group-image", "Config": map[string]any{"Labels": map[string]string{"org.gopod.profile": "group", "org.gopod.provider": "claude", "org.gopod.native-policy": runner.GroupNativePolicyVersion}}})
	case strings.HasSuffix(r.URL.Path, "/networks"):
		io.WriteString(w, `[]`)
	default:
		w.WriteHeader(404)
		io.WriteString(w, `{"message":"unexpected route"}`)
	}
}
func migrationManager(t *testing.T) (*Manager, *migrationDocker) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	st, err := store.Open(t.Context(), filepath.Join(base, "store.sqlite"), log)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	for _, c := range []store.RegisteredChat{{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}, {JID: "tg:-2", Folder: "old-group", AddedAt: 2}} {
		if err = st.RegisterChat(t.Context(), c); err != nil {
			t.Fatal(err)
		}
	}
	if err = st.UpsertChat(t.Context(), store.ChatRecord{JID: "tg:-2", IsGroup: true, LastMessageTime: 1}); err != nil {
		t.Fatal(err)
	}
	fake := &migrationDocker{}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	docker, err := client.NewClientWithOpts(client.WithHost(srv.URL), client.WithHTTPClient(srv.Client()), client.WithVersion("1.47"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { docker.Close() })
	return &Manager{Store: st, Docker: docker, DataDir: base, UID: os.Geteuid(), GID: os.Getegid(), Log: log}, fake
}
func TestPrepareResumesInterruptedEpochAndPreservesOldData(t *testing.T) {
	m, fake := migrationManager(t)
	ctx := t.Context()
	c, _ := m.Store.GetRegistered(ctx, "tg:-2")
	owner, _ := m.Store.SnapshotScope(ctx, "owner")
	migration, err := m.Store.RotateGroupEpoch(ctx, c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate the controller dying after its DB commit, before file preparation.
	if _, err = m.Prepare(ctx, c.JID); err != nil {
		t.Fatal(err)
	}
	after, _ := m.Store.GetRegistered(ctx, c.JID)
	if after.Scope() != migration.CurrentScope || after.GroupEpochReady {
		t.Fatal("retry rotated or enabled epoch")
	}
	paths, err := runner.ValidateNamespacePaths(m.DataDir, after.Scope(), m.UID)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"CLAUDE.md", "AGENTS.md", "SCHEDULING.md"} {
		if _, err = os.Stat(filepath.Join(paths.Workspace, name)); err != nil {
			t.Fatal("clean root not seeded")
		}
	}
	if _, err = m.Prepare(ctx, c.JID); err != nil {
		t.Fatal("repeated preparation failed")
	}
	afterAgain, _ := m.Store.GetRegistered(ctx, c.JID)
	if afterAgain.Scope() != after.Scope() {
		t.Fatal("repeated prepare changed identity")
	}
	if m.Store.ValidateScope(ctx, owner) != nil {
		t.Fatal("personal namespace changed")
	}
	disabled, _ := m.Store.GetState(ctx, "group_disabled:"+c.NamespaceID)
	if disabled != "1" {
		t.Fatal("incomplete preparation enabled runtime")
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.mutations != 0 {
		t.Fatal("migration killed/deleted Docker resources")
	}
	if !strings.Contains(strings.Join(fake.seen, " "), "gopod.chat=old-group") {
		t.Fatal("retired legacy key not inspected")
	}
}
func TestLegacyContainerBlocksPrepareAndEnableAcrossRetiredKeys(t *testing.T) {
	m, fake := migrationManager(t)
	ctx := t.Context()
	c, _ := m.Store.GetRegistered(ctx, "tg:-2")
	fake.legacy = true
	if _, err := m.Prepare(ctx, c.JID); err == nil {
		t.Fatal("legacy process did not block prepare")
	}
	unchanged, _ := m.Store.GetRegistered(ctx, c.JID)
	if unchanged.Scope() != c.Scope() {
		t.Fatal("blocked prepare rotated state")
	}
	fake.legacy = false
	migration, err := m.Store.RotateGroupEpoch(ctx, c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	paths, err := runner.PrepareNamespacePaths(m.DataDir, migration.CurrentScope, m.UID)
	if err != nil {
		t.Fatal(err)
	}
	auth := filepath.Join(m.DataDir, "synthetic-auth.json")
	if err = os.WriteFile(auth, []byte("synthetic-not-a-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Config = Config{Qualification: "synthetic-qualified", EnabledGroups: []string{c.JID}, Providers: map[string]ProviderConfig{"claude": {Image: "group-image", AuthFile: auth}}}
	fake.legacy = true
	if _, err = m.Enable(ctx, c.JID); err == nil {
		t.Fatal("retired-key legacy process did not block enable")
	}
	current, _ := m.Store.GetRegistered(ctx, c.JID)
	if current.GroupEpochReady {
		t.Fatal("blocked enable qualified group")
	}
	fake.legacy = false
	if _, err = m.Enable(ctx, c.JID); err != nil {
		t.Fatalf("positive control enable failed: %v", err)
	}
	current, _ = m.Store.GetRegistered(ctx, c.JID)
	if !current.GroupEpochReady {
		t.Fatal("qualified control not ready")
	}
	policy, err := os.ReadDir(paths.Policy)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range policy {
		if strings.HasPrefix(entry.Name(), "auth-ref-") {
			t.Fatal("qualification left auth reference")
		}
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.mutations != 0 {
		t.Fatal("enable killed/deleted Docker resources")
	}
}
