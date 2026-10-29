package runner

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/Coriol-is/gopod/internal/store"
)

// fakeScopes is the host scope source: one revision per folder.
type fakeScopes struct {
	mu  sync.Mutex
	rev map[string]int64
	err error
}

func (f *fakeScopes) SnapshotScope(_ context.Context, folder string) (store.ScopeRef, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return store.ScopeRef{}, f.err
	}
	rev := f.rev[folder]
	if rev == 0 {
		rev = 1
	}
	return store.ScopeRef{
		NamespaceID: "11111111-2222-4333-8444-555555555555", NamespaceEpoch: 1,
		RegistrationRevision: rev, JID: "tg:1", Folder: folder,
	}, nil
}

func (f *fakeScopes) bump(folder string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.rev == nil {
		f.rev = map[string]int64{}
	}
	if f.rev[folder] == 0 {
		f.rev[folder] = 1
	}
	f.rev[folder]++
}

// fakeEngine models the Docker daemon for the guard: containers keep the
// configuration they were created with, like a real inspection reports it.
type fakeEngine struct {
	mu         sync.Mutex
	containers map[string]*ContainerFacts // by name
	images     map[string]string          // ref -> id
	ops        []string
	nextID     int
	inspectErr error
}

func newFakeEngine() *fakeEngine {
	return &fakeEngine{containers: map[string]*ContainerFacts{}, images: map[string]string{}}
}

func (e *fakeEngine) op(s string) { e.ops = append(e.ops, s) }

func (e *fakeEngine) opsSnapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.ops...)
}

func (e *fakeEngine) InspectContainer(_ context.Context, name string) (*ContainerFacts, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.inspectErr != nil {
		return nil, e.inspectErr
	}
	f, ok := e.containers[name]
	if !ok {
		return nil, nil
	}
	cp := *f
	return &cp, nil
}

func (e *fakeEngine) ImageID(_ context.Context, ref string) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id, ok := e.images[ref]; ok {
		return id, nil
	}
	return "sha256:" + ref, nil
}

func (e *fakeEngine) StopRemove(_ context.Context, id string) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.op("stop_remove:" + id)
	for n, f := range e.containers {
		if f.ID == id {
			delete(e.containers, n)
		}
	}
	return nil
}

func (e *fakeEngine) CreateStart(_ context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.containers[name]; ok {
		return "", errors.New("name in use")
	}
	e.nextID++
	id := "c" + string(rune('0'+e.nextID))
	f := &ContainerFacts{
		ID: id, Running: true, Inspected: true,
		ImageID: "sha256:" + cfg.Image,
		Labels:  map[string]string{},
		EnvNames: append(envNamesOf(cfg.Env),
			"PATH", "NODE_VERSION"), // image-baked names the guard must ignore
		User:           cfg.User,
		ReadonlyRootfs: host.ReadonlyRootfs,
		Privileged:     host.Privileged,
		CapDrop:        []string(host.CapDrop),
		SecurityOpt:    host.SecurityOpt,
	}
	for k, v := range cfg.Labels {
		f.Labels[k] = v
	}
	if id, ok := e.images[cfg.Image]; ok {
		f.ImageID = id
	}
	for _, m := range host.Mounts {
		f.Mounts = append(f.Mounts, Mount{Source: m.Source, Target: m.Target, ReadOnly: m.ReadOnly})
	}
	e.containers[name] = f
	e.op("create:" + f.Labels[LabelPolicyRole])
	return id, nil
}

func (e *fakeEngine) get(name string) *ContainerFacts {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.containers[name]
}

// managedTestRunner is a Runner with the managed runtime on, a fake daemon and a
// real directory layout.
type managedTestRunner struct {
	r      *Runner
	eng    *fakeEngine
	scopes *fakeScopes
	paths  Paths
	holds  map[string]bool
}

func newManagedTestRunner(t *testing.T, prov AgentProvider, qualify TaskRoleQualifier) *managedTestRunner {
	t.Helper()
	paths := mkPaths(t)
	eng := newFakeEngine()
	scopes := &fakeScopes{}
	m := &managedTestRunner{eng: eng, scopes: scopes, paths: paths, holds: map[string]bool{}}
	r := &Runner{
		profilePolicy:  testPersonalPolicy(),
		provider:       prov,
		paths:          paths,
		cfg:            SpawnDefaults{Image: prov.Image(), UID: 501, GID: 20, MemoryBytes: 4 << 30, NanoCPUs: 2e9, PidsLimit: 1024},
		allow:          []string{"FREEFEED_BASE_URL"},
		version:        "test",
		log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		containerLocks: map[string]*sync.Mutex{},
		activity:       map[string]time.Time{},
		turnCount:      map[convKey]int{},
	}
	if err := r.SetManagedTasks(ManagedTaskConfig{
		Scopes:    scopes,
		Holds:     func(f string) bool { return m.holds[f] },
		Qualifier: qualify,
		Providers: map[string]AgentProvider{prov.Name(): prov},
	}); err != nil {
		t.Fatal(err)
	}
	r.managed.engine = eng
	m.r = r
	return m
}

// allowShared qualifies the shared native state dir binding for tests only.
func allowShared(string, CredentialBinding) error { return nil }

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
