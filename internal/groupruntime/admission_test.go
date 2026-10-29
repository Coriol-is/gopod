package groupruntime

import (
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func admissionFixture(t *testing.T) (*Manager, store.RegisteredChat) {
	t.Helper()
	m, scope, _ := receiptRequestFixture(t)
	m.Config.EnabledGroups = nil
	c, err := m.Store.GetRegistered(t.Context(), scope.JID)
	if err != nil {
		t.Fatal(err)
	}
	return m, c
}

func TestRegisteredModeAdmitsWithoutAllowlist(t *testing.T) {
	m, c := admissionFixture(t)
	if err := m.Authorize(t.Context(), c); err != nil {
		t.Fatalf("registered group with ready epoch denied: %v", err)
	}
	if got := m.Reason(t.Context(), c.JID); got != "" {
		t.Fatalf("unexpected reason %q", got)
	}
}

func TestRegisteredModeDenialMatrix(t *testing.T) {
	cases := map[string]func(*testing.T, *Manager, *store.RegisteredChat){
		"epoch not ready": func(_ *testing.T, _ *Manager, c *store.RegisteredChat) { c.GroupEpochReady = false },
		"owner chat":      func(_ *testing.T, _ *Manager, c *store.RegisteredChat) { c.IsOwner = true },
		"malformed jid":   func(_ *testing.T, _ *Manager, c *store.RegisteredChat) { c.JID = "tg:-x" },
		"unknown group":   func(_ *testing.T, _ *Manager, c *store.RegisteredChat) { c.JID = "tg:-999" },
		"no qualification": func(_ *testing.T, m *Manager, _ *store.RegisteredChat) {
			m.Config.Qualification = ""
		},
		"disabled": func(t *testing.T, m *Manager, c *store.RegisteredChat) {
			if err := m.Store.SetNamespaceDisabled(t.Context(), c.Scope(), true); err != nil {
				t.Fatal(err)
			}
		},
		"cleanup unknown": func(t *testing.T, m *Manager, c *store.RegisteredChat) {
			if err := m.Store.SetState(t.Context(), "group_cleanup_unknown:"+c.NamespaceID, "1"); err != nil {
				t.Fatal(err)
			}
		},
		"unknown mode": func(_ *testing.T, m *Manager, _ *store.RegisteredChat) { m.Config.AdmissionMode = "open" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m, c := admissionFixture(t)
			mutate(t, m, &c)
			if m.Authorize(t.Context(), c) == nil {
				t.Fatal("admitted")
			}
		})
	}
}

func TestRegisteredModeCapsPreparedGroups(t *testing.T) {
	m, c := admissionFixture(t)
	ctx := t.Context()
	older := store.RegisteredChat{JID: "tg:-3", Folder: "older", AddedAt: 1}
	if err := m.Store.RegisterChat(ctx, older); err != nil {
		t.Fatal(err)
	}
	stored, _ := m.Store.GetRegistered(ctx, older.JID)
	if _, err := m.Store.RotateGroupEpoch(ctx, older.JID, stored.RegistrationRevision); err != nil {
		t.Fatal(err)
	}
	m.Config.MaxGroups = 1
	if m.Authorize(ctx, c) == nil {
		t.Fatal("group beyond the prepared-group cap admitted")
	}
	if got := m.Reason(ctx, c.JID); got != "over prepared-group limit" {
		t.Fatalf("reason %q", got)
	}
	m.Config.MaxGroups = 2
	if err := m.Authorize(ctx, c); err != nil {
		t.Fatalf("within cap denied: %v", err)
	}
	if m.canPrepare(ctx, store.RegisteredChat{NamespaceEpoch: 1}) {
		t.Fatal("cap does not bound new preparations")
	}
}

func TestAllowlistModeKeepsStrictBehavior(t *testing.T) {
	m, c := admissionFixture(t)
	m.Config.AdmissionMode = AdmissionAllowlist
	if m.Authorize(t.Context(), c) == nil {
		t.Fatal("unlisted group admitted in allowlist mode")
	}
	m.Config.EnabledGroups = []string{c.JID}
	if err := m.Authorize(t.Context(), c); err != nil {
		t.Fatalf("listed group denied: %v", err)
	}
	if m.Config.registered() || m.canPrepare(t.Context(), store.RegisteredChat{NamespaceEpoch: 1}) != true {
		t.Fatal("allowlist mode must not apply the registered-mode cap")
	}
}

func TestDisableRevokesImmediatelyAndBlocksAutoPrepare(t *testing.T) {
	m, c := admissionFixture(t)
	ctx := t.Context()
	if _, err := m.Disable(ctx, c.JID); err != nil {
		t.Fatal(err)
	}
	if m.Authorize(ctx, c) == nil {
		t.Fatal("disabled group still admitted")
	}
	if got := m.Reason(ctx, c.JID); got != "disabled by owner" {
		t.Fatalf("reason %q", got)
	}
	// An unready, owner-disabled group is not a candidate for automatic preparation.
	if _, err := m.autoCandidate(ctx, c.JID); err == nil {
		t.Fatal("ready group became an auto candidate")
	}
}

func TestLoadValidatesAdmissionSettings(t *testing.T) {
	for _, tc := range []struct {
		cfg Config
		ok  bool
	}{
		{Config{}, true},
		{Config{AdmissionMode: "registered", MaxGroups: 5}, true},
		{Config{AdmissionMode: "allowlist"}, true},
		{Config{AdmissionMode: "open"}, false},
		{Config{MaxGroups: -1}, false},
		{Config{MaxGroups: 1000}, false},
	} {
		if tc.cfg.validAdmission() != tc.ok {
			t.Fatalf("%+v ok=%v", tc.cfg, tc.ok)
		}
	}
	if (Config{}).GroupLimit() != DefaultMaxGroups || (Config{MaxGroups: 3}).GroupLimit() != 3 {
		t.Fatal("group limit")
	}
}
