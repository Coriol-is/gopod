package groupruntime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Coriol-is/gopod/internal/store"
)

type noticeLog struct {
	mu    sync.Mutex
	texts []string
}

func (n *noticeLog) add(_ context.Context, text string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.texts = append(n.texts, text)
}
func (n *noticeLog) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.texts...)
}

// autoFixture is a registered, observed group at epoch 1 (never prepared) under a
// config with an explicit-grant claude provider and no EnabledGroups entry.
func autoFixture(t *testing.T) (*Manager, *migrationDocker, *noticeLog, store.RegisteredChat) {
	t.Helper()
	m, fake := migrationManager(t)
	auth := filepath.Join(m.DataDir, "synthetic-auth.json")
	if err := os.WriteFile(auth, []byte("synthetic-not-a-credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	m.Config = Config{Qualification: "synthetic-qualified", Providers: map[string]ProviderConfig{"claude": {Image: "group-image", AuthFile: auth}}}
	notes := &noticeLog{}
	m.Notify = notes.add
	m.auto.spawn = func(f func()) { f() }
	m.MarkReady()
	c, err := m.Store.GetRegistered(t.Context(), "tg:-2")
	if err != nil {
		t.Fatal(err)
	}
	return m, fake, notes, c
}

func TestEnsurePreparedPreparesAndEnablesNewGroup(t *testing.T) {
	m, fake, notes, c := autoFixture(t)
	ctx := t.Context()
	slots := 0
	m.Slot = func(ctx context.Context, folder string, op func(context.Context) (string, error)) (string, error) {
		slots++
		if folder != c.Folder {
			t.Fatalf("slot taken for %q", folder)
		}
		return op(ctx)
	}
	text, err := m.EnsurePrepared(ctx, c.JID, true)
	if err != nil || !strings.Contains(text, "old state retired not deleted") {
		t.Fatalf("not prepared: %q %v", text, err)
	}
	got, _ := m.Store.GetRegistered(ctx, c.JID)
	if !got.GroupEpochReady || got.NamespaceEpoch != 2 || slots != 1 {
		t.Fatalf("ready=%v epoch=%d slots=%d", got.GroupEpochReady, got.NamespaceEpoch, slots)
	}
	if v, _ := m.Store.GetState(ctx, "group_disabled:"+got.NamespaceID); v != "0" {
		t.Fatalf("group still disabled: %q", v)
	}
	if reason := m.Reason(ctx, c.JID); reason != "" {
		t.Fatalf("not admissible: %s", reason)
	}
	if n := notes.all(); len(n) != 1 || !strings.Contains(n[0], c.JID) {
		t.Fatalf("notices: %v", n)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if fake.mutations != 0 {
		t.Fatal("preparation touched Docker resources")
	}
}

func TestEnsurePreparedIsIdempotent(t *testing.T) {
	m, _, notes, c := autoFixture(t)
	for i := 0; i < 3; i++ {
		if _, err := m.EnsurePrepared(t.Context(), c.JID, false); err != nil {
			t.Fatal(err)
		}
	}
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if got.NamespaceEpoch != 2 || len(notes.all()) != 1 {
		t.Fatalf("epoch=%d notices=%d", got.NamespaceEpoch, len(notes.all()))
	}
}

func TestEnsurePreparedResumesInterruptedEpoch(t *testing.T) {
	m, _, _, c := autoFixture(t)
	ctx := t.Context()
	migration, err := m.Store.RotateGroupEpoch(ctx, c.JID, c.RegistrationRevision)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.EnsurePrepared(ctx, c.JID, false); err != nil {
		t.Fatal(err)
	}
	got, _ := m.Store.GetRegistered(ctx, c.JID)
	if got.NamespaceEpoch != migration.CurrentScope.NamespaceEpoch || !got.GroupEpochReady {
		t.Fatalf("resume rotated again: epoch=%d ready=%v", got.NamespaceEpoch, got.GroupEpochReady)
	}
}

func TestEnsurePreparedConcurrentCallsShareOneEpoch(t *testing.T) {
	m, _, notes, c := autoFixture(t)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = m.EnsurePrepared(t.Context(), c.JID, false)
		}()
	}
	wg.Wait()
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if got.NamespaceEpoch != 2 || !got.GroupEpochReady || len(notes.all()) != 1 {
		t.Fatalf("epoch=%d ready=%v notices=%d", got.NamespaceEpoch, got.GroupEpochReady, len(notes.all()))
	}
}

func TestEnsurePreparedFailsClosedWithBackoffAndOneNotice(t *testing.T) {
	m, fake, notes, c := autoFixture(t)
	ctx := t.Context()
	now := time.Unix(1000, 0)
	m.auto.now = func() time.Time { return now }
	fake.legacy = true
	if _, err := m.EnsurePrepared(ctx, c.JID, false); err == nil {
		t.Fatal("blocked preparation reported success")
	}
	got, _ := m.Store.GetRegistered(ctx, c.JID)
	if got.GroupEpochReady || m.Authorize(ctx, got) == nil {
		t.Fatal("failed preparation admitted the group")
	}
	fake.mu.Lock()
	seen := len(fake.seen)
	fake.mu.Unlock()
	if _, err := m.EnsurePrepared(ctx, c.JID, false); err == nil {
		t.Fatal("backoff did not deny")
	}
	fake.mu.Lock()
	if len(fake.seen) != seen {
		t.Fatal("backoff still queried Docker")
	}
	fake.mu.Unlock()
	now = now.Add(time.Minute)
	if _, err := m.EnsurePrepared(ctx, c.JID, true); err == nil {
		t.Fatal("forced retry should still fail while blocked")
	}
	if n := notes.all(); len(n) != 1 || strings.Contains(n[0], "synthetic") {
		t.Fatalf("failure notice not rate limited or leaky: %v", n)
	}
	fake.legacy = false
	if _, err := m.EnsurePrepared(ctx, c.JID, true); err != nil {
		t.Fatalf("recovery failed: %v", err)
	}
}

func TestEnsurePreparedSkipsOwnerDisabledAndAllowlistMode(t *testing.T) {
	m, _, notes, c := autoFixture(t)
	ctx := t.Context()
	if err := m.Store.SetState(ctx, groupOwnerDisabledKey+c.NamespaceID, "1"); err != nil {
		t.Fatal(err)
	}
	if text, err := m.EnsurePrepared(ctx, c.JID, true); text != "" || err != nil {
		t.Fatal("owner-disabled group was prepared")
	}
	if err := m.Store.SetState(ctx, groupOwnerDisabledKey+c.NamespaceID, "0"); err != nil {
		t.Fatal(err)
	}
	m.Config.AdmissionMode = AdmissionAllowlist
	if text, err := m.EnsurePrepared(ctx, c.JID, true); text != "" || err != nil {
		t.Fatal("allowlist mode prepared automatically")
	}
	got, _ := m.Store.GetRegistered(ctx, c.JID)
	if got.NamespaceEpoch != 1 || len(notes.all()) != 0 {
		t.Fatal("no-op mutated state")
	}
}

func TestEnsurePreparedRespectsGroupCap(t *testing.T) {
	m, _, _, c := autoFixture(t)
	m.Config.MaxGroups = 1
	other := store.RegisteredChat{JID: "tg:-3", Folder: "other", AddedAt: 1}
	if err := m.Store.RegisterChat(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	stored, _ := m.Store.GetRegistered(t.Context(), other.JID)
	if _, err := m.Store.RotateGroupEpoch(t.Context(), other.JID, stored.RegistrationRevision); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsurePrepared(t.Context(), c.JID, true); err == nil {
		t.Fatal("cap did not stop a new preparation")
	}
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if got.NamespaceEpoch != 1 {
		t.Fatal("capped group rotated")
	}
}

func TestKickPrepareAndReconcileAll(t *testing.T) {
	m, _, _, c := autoFixture(t)
	ctx := t.Context()
	m.KickPrepare(c)
	got, _ := m.Store.GetRegistered(ctx, c.JID)
	if !got.GroupEpochReady {
		t.Fatal("first admission attempt did not reconcile")
	}
	// ReconcileAll over an already ready set is a no-op.
	m.ReconcileAll(ctx)
	again, _ := m.Store.GetRegistered(ctx, c.JID)
	if again.NamespaceEpoch != got.NamespaceEpoch {
		t.Fatal("reconcile rotated a ready group")
	}
}

func TestReconcileAllPreparesUnreadyGroups(t *testing.T) {
	m, _, _, c := autoFixture(t)
	m.ReconcileAll(t.Context())
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if !got.GroupEpochReady {
		t.Fatal("startup reconcile did not prepare")
	}
}

func TestPreparationWaitsForStartupGate(t *testing.T) {
	m, _, _, c := autoFixture(t)
	m.ready.Store(false)
	if _, err := m.EnsurePrepared(t.Context(), c.JID, true); err == nil {
		t.Fatal("prepared before startup finished")
	}
	m.KickPrepare(c)
	m.ReconcileAll(t.Context())
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if got.NamespaceEpoch != 1 {
		t.Fatal("epoch rotated before the startup gate opened")
	}
}

func TestKickFloodSpawnsOneRunAndNoDatabaseReads(t *testing.T) {
	m, fake, _, c := autoFixture(t)
	var mu sync.Mutex
	var pending []func()
	spawned := 0
	m.auto.spawn = func(f func()) { mu.Lock(); spawned++; pending = append(pending, f); mu.Unlock() }
	for i := 0; i < 2000; i++ {
		m.KickPrepare(c)
	}
	if spawned != 1 {
		t.Fatalf("flood spawned %d runs", spawned)
	}
	fake.mu.Lock()
	reads := len(fake.seen)
	fake.mu.Unlock()
	if reads != 0 {
		t.Fatalf("kicks queried Docker %d times", reads)
	}
	pending[0]()
	m.KickPrepare(mustRegistered(t, m, c.JID)) // ready now: no-op
	got, _ := m.Store.GetRegistered(t.Context(), c.JID)
	if !got.GroupEpochReady || spawned != 1 {
		t.Fatalf("ready=%v spawned=%d", got.GroupEpochReady, spawned)
	}
}

func TestKickAfterFailureHonorsBackoffWithoutSpawning(t *testing.T) {
	m, fake, _, c := autoFixture(t)
	now := time.Unix(5000, 0)
	m.auto.now = func() time.Time { return now }
	fake.legacy = true
	m.KickPrepare(c) // inline spawn: fails and records the failure
	spawned := 0
	m.auto.spawn = func(f func()) { spawned++ }
	for i := 0; i < 100; i++ {
		m.KickPrepare(c)
	}
	if spawned != 0 {
		t.Fatal("kick spawned during backoff")
	}
	now = now.Add(2 * autoRetryBackoff)
	m.KickPrepare(c)
	if spawned != 1 {
		t.Fatal("kick not allowed after backoff")
	}
}

func TestUnobservedGroupWaitsSilently(t *testing.T) {
	m, _, notes, c := autoFixture(t)
	other := store.RegisteredChat{JID: "tg:-9", Folder: "unseen", AddedAt: 9}
	if err := m.Store.RegisterChat(t.Context(), other); err != nil {
		t.Fatal(err)
	}
	if _, err := m.EnsurePrepared(t.Context(), other.JID, true); !errors.Is(err, ErrNotObserved) {
		t.Fatalf("err=%v", err)
	}
	m.KickPrepare(mustRegistered(t, m, other.JID))
	got, _ := m.Store.GetRegistered(t.Context(), other.JID)
	if got.NamespaceEpoch != 1 || len(notes.all()) != 0 {
		t.Fatal("unobserved group rotated or owner notified")
	}
	_ = c
}

func mustRegistered(t *testing.T, m *Manager, jid string) store.RegisteredChat {
	t.Helper()
	c, err := m.Store.GetRegistered(t.Context(), jid)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestUnqualifiedConfigNeverRotatesEpoch(t *testing.T) {
	for name, mutate := range map[string]func(*Manager){
		"no qualification": func(m *Manager) { m.Config.Qualification = "" },
		"no providers":     func(m *Manager) { m.Config.Providers = nil },
	} {
		t.Run(name, func(t *testing.T) {
			m, _, _, c := autoFixture(t)
			mutate(m)
			if text, err := m.EnsurePrepared(t.Context(), c.JID, true); text != "" || err != nil {
				t.Fatalf("%q %v", text, err)
			}
			m.ReconcileAll(t.Context())
			if got := mustRegistered(t, m, c.JID); got.NamespaceEpoch != 1 {
				t.Fatal("epoch rotated although enable can never succeed")
			}
		})
	}
}

// The owner disables between preparation and enabling: the automatic path must
// not re-enable, whatever the earlier marker check saw.
func TestAutoEnableAbortsWhenOwnerDisablesAfterPrepare(t *testing.T) {
	m, _, _, c := autoFixture(t)
	ctx := t.Context()
	if _, err := m.prepareCore(ctx, c.JID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Disable(ctx, c.JID); err != nil {
		t.Fatal(err)
	}
	if _, err := m.enableCore(ctx, c.JID, true); err == nil {
		t.Fatal("automatic enable overrode the owner kill switch")
	}
	got := mustRegistered(t, m, c.JID)
	if v, _ := m.Store.GetState(ctx, "group_disabled:"+got.NamespaceID); v != "1" {
		t.Fatalf("group_disabled=%q", v)
	}
	if m.Authorize(ctx, got) == nil {
		t.Fatal("owner-disabled group admitted")
	}
	// Manual enable by the owner still works and clears the marker.
	if _, err := m.Enable(ctx, c.JID); err != nil {
		t.Fatalf("manual enable: %v", err)
	}
	if v, _ := m.Store.GetState(ctx, "group_disabled:"+got.NamespaceID); v != "0" {
		t.Fatal("manual enable did not reopen admission")
	}
}

func TestStoppedLegacyContainerIsRemovedAndRunningOneBlocks(t *testing.T) {
	m, fake, notes, c := autoFixture(t)
	fake.stopped = true
	if _, err := m.EnsurePrepared(t.Context(), c.JID, true); err != nil {
		t.Fatalf("stopped legacy container blocked preparation: %v", err)
	}
	fake.mu.Lock()
	removed := append([]string(nil), fake.removed...)
	mutations := fake.mutations
	fake.mu.Unlock()
	if len(removed) != 1 || mutations != 0 {
		t.Fatalf("removed=%v mutations=%d", removed, mutations)
	}
	if got := mustRegistered(t, m, c.JID); !got.GroupEpochReady {
		t.Fatal("not prepared")
	}
	_ = notes

	m2, fake2, notes2, c2 := autoFixture(t)
	fake2.legacy = true
	if _, err := m2.EnsurePrepared(t.Context(), c2.JID, true); err == nil {
		t.Fatal("running legacy container did not block")
	}
	fake2.mu.Lock()
	if len(fake2.removed) != 0 || fake2.mutations != 0 {
		t.Fatal("running container was touched")
	}
	fake2.mu.Unlock()
	n := notes2.all()
	if len(n) != 1 || !strings.Contains(n[0], "Blocking containers: legacy-runni") {
		t.Fatalf("owner note lacks sanitized blocker: %v", n)
	}
}
