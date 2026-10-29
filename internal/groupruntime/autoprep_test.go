package groupruntime

import (
	"context"
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

func TestAuthorizeKicksPreparationAndReconcileAll(t *testing.T) {
	m, _, _, c := autoFixture(t)
	ctx := t.Context()
	if m.Authorize(ctx, c) == nil {
		t.Fatal("unready group admitted")
	}
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
