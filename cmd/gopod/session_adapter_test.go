package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

func sessionFixture(t *testing.T) (*store.Store, *durableSessionStore) {
	t.Helper()
	ctx := t.Context()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "store.sqlite"), nil)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	if err := st.RegisterChat(ctx, store.RegisteredChat{JID: "tg:1", Folder: "owner", IsOwner: true, AddedAt: 1}); err != nil {
		t.Fatalf("register chat: %v", err)
	}
	return st, &durableSessionStore{st: st}
}

func bindingFor(t *testing.T, ctx context.Context, st *store.Store, folder string, thread int) store.ConversationBinding {
	t.Helper()
	scope, err := st.SnapshotScope(ctx, folder)
	if err != nil {
		t.Fatalf("snapshot scope: %v", err)
	}
	b, err := st.ResolveConversation(ctx, scope, thread)
	if err != nil {
		t.Fatalf("resolve conversation: %v", err)
	}
	return b
}

func TestDurableAdapterRoundTripsThroughBinding(t *testing.T) {
	ctx := t.Context()
	st, ad := sessionFixture(t)
	if err := ad.PutSession(ctx, "owner", 0, "claude", "native-1"); err != nil {
		t.Fatalf("put: %v", err)
	}
	id, ok, err := ad.GetSession(ctx, "owner", 0, "claude")
	if err != nil || !ok || id != "native-1" {
		t.Fatalf("get = %q %v %v", id, ok, err)
	}
	b := bindingFor(t, ctx, st, "owner", 0)
	got, ok, err := st.GetNativeContext(ctx, b.ActiveSessionID, b.ContextEpoch, "claude")
	if err != nil || !ok || got != "native-1" {
		t.Fatalf("native context = %q %v %v", got, ok, err)
	}
}

func TestDurableAdapterDeleteRemovesOnlyProviderContext(t *testing.T) {
	ctx := t.Context()
	st, ad := sessionFixture(t)
	fakeDockerDaemon(t)
	for p, id := range map[string]string{"claude": "c-1", "codex": "x-1"} {
		if err := ad.PutSession(ctx, "owner", 0, p, id); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	before := bindingFor(t, ctx, st, "owner", 0)
	if err := ad.DeleteSession(ctx, "owner", 0, "claude"); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok, err := ad.GetSession(ctx, "owner", 0, "claude"); err != nil || ok {
		t.Fatalf("claude after delete: ok=%v err=%v", ok, err)
	}
	if id, ok, err := ad.GetSession(ctx, "owner", 0, "codex"); err != nil || !ok || id != "x-1" {
		t.Fatalf("codex = %q %v %v", id, ok, err)
	}
	after := bindingFor(t, ctx, st, "owner", 0)
	if after.ActiveSessionID != before.ActiveSessionID || after.ContextEpoch != before.ContextEpoch {
		t.Fatalf("binding changed: %+v -> %+v", before, after)
	}
}

func TestDurableAdapterRejectsUnregisteredFolder(t *testing.T) {
	ctx := t.Context()
	_, ad := sessionFixture(t)
	if _, ok, err := ad.GetSession(ctx, "nope", 0, "claude"); !errors.Is(err, store.ErrStaleScope) || ok {
		t.Fatalf("get err = %v ok=%v, want ErrStaleScope", err, ok)
	}
	if err := ad.PutSession(ctx, "nope", 0, "claude", "x"); !errors.Is(err, store.ErrStaleScope) {
		t.Fatalf("put err = %v", err)
	}
}

func TestNewSessionStateStoreSelectsByMarker(t *testing.T) {
	ctx := t.Context()
	st, _ := sessionFixture(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, mode, err := newSessionStateStore(ctx, st, log)
	if err != nil {
		t.Fatalf("legacy: %v", err)
	}
	if _, ok := got.(*store.Store); !ok || mode != store.SessionMappingLegacy {
		t.Fatalf("legacy got %T mode %q", got, mode)
	}
	if _, err := st.AdoptLegacySessions(ctx); err != nil {
		t.Fatalf("adopt: %v", err)
	}
	got, mode, err = newSessionStateStore(ctx, st, log)
	if err != nil {
		t.Fatalf("durable: %v", err)
	}
	if _, ok := got.(*durableSessionStore); !ok || mode != store.SessionMappingDurable {
		t.Fatalf("durable got %T mode %q", got, mode)
	}
}

func TestDurableAdapterImplementsContinuity(t *testing.T) {
	ctx := t.Context()
	st, ad := sessionFixture(t)
	fakeDockerDaemon(t)
	for p, id := range map[string]string{"claude": "c-1", "codex": "x-1"} {
		if err := ad.PutSession(ctx, "owner", 0, p, id); err != nil {
			t.Fatalf("put %s: %v", p, err)
		}
	}
	if err := st.PutSession(ctx, "owner", 0, "claude", "legacy-frozen"); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	paths := runner.Paths{RepoRoot: dir, DataDir: dir, ChatsDir: dir, ContainerSkillsDir: dir, EmptyFile: filepath.Join(dir, "empty")}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, err := runner.NewDocker(ctx, log)
	if err != nil {
		t.Fatalf("NewDocker against fake daemon: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	r, err := runner.New(d, runner.NewClaudeProvider("img"), paths, runner.SpawnDefaults{}, nil, "test", log)
	if err != nil {
		t.Fatalf("runner.New: %v", err)
	}
	r.SetStore(ad)
	r.SetAdmissionPolicy(1, st)
	// Without continuity support the runner falls back to the legacy path,
	// which execs a clear in a container (unexpected docker call) and clears
	// only the active provider; both contexts gone proves the assertion.
	if err := r.ClearSession(ctx, "owner", 0, runner.TierOwner, nil); err != nil {
		t.Fatalf("ClearSession: %v", err)
	}
	for _, p := range []string{"claude", "codex"} {
		if _, ok, err := ad.GetSession(ctx, "owner", 0, p); err != nil || ok {
			t.Fatalf("%s context survived /clear: ok=%v err=%v", p, ok, err)
		}
	}
	if id, ok, err := st.GetSession(ctx, "owner", 0, "claude"); err != nil || !ok || id != "legacy-frozen" {
		t.Fatalf("legacy sessions row = %q %v %v, want untouched", id, ok, err)
	}
	if v, err := st.GetState(ctx, "continuity:owner:0:seeded"); err != nil || v != "1" {
		t.Fatalf("seeded marker = %q %v", v, err)
	}
}

// fakeDockerDaemon points DOCKER_HOST at an HTTP server that answers the ping
// and reports no containers, so the runner's slot and quiescence checks run
// without a real daemon.
func fakeDockerDaemon(t *testing.T) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Api-Version", "1.47")
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/_ping"):
			_, _ = io.WriteString(w, "OK")
		case strings.HasSuffix(r.URL.Path, "/containers/json"):
			_, _ = io.WriteString(w, "[]")
		default:
			t.Errorf("unexpected docker call %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	t.Setenv("DOCKER_HOST", "tcp://"+strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("DOCKER_TLS_VERIFY", "")
	t.Setenv("DOCKER_CERT_PATH", "")
	t.Setenv("DOCKER_API_VERSION", "")
}
