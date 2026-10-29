package runner

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Coriol-is/gopod/internal/store"
)

func basePolicy() EffectiveExecutionPolicy {
	return EffectiveExecutionPolicy{
		Role:           RoleScheduledTaskPersonal,
		Scope:          store.ScopeRef{NamespaceID: "ns", NamespaceEpoch: 1, RegistrationRevision: 1, JID: "tg:1", Folder: "owner"},
		PolicyRevision: policyRevision,
		Mounts: []Mount{
			{Source: "/d/chat", Target: "/workspace/chat"},
			{Source: "/d/ipc", Target: "/workspace/ipc"},
		},
		EnvNames:    []string{"A", "B"},
		Credentials: CredentialBinding{Provider: "claude", Kind: credNativeStateDir, Sources: []string{"/d/s"}},
		Image:       "img:1",
		Grants:      []string{"x", "y"},
	}
}

func TestFingerprintIsDeterministicAndOrderInsensitive(t *testing.T) {
	a := basePolicy()
	b := basePolicy()
	b.Mounts = []Mount{b.Mounts[1], b.Mounts[0]}
	b.EnvNames = []string{"B", "A", "A"}
	b.Grants = []string{"y", "x"}
	if a.Fingerprint() != b.Fingerprint() {
		t.Fatal("equal policies must have equal fingerprints regardless of ordering")
	}
	if !strings.HasPrefix(a.Fingerprint(), "epp1:") {
		t.Fatalf("fingerprint %q lacks version prefix", a.Fingerprint())
	}
}

func TestFingerprintCoversEveryAuthorityField(t *testing.T) {
	base := basePolicy().Fingerprint()
	for name, mut := range map[string]func(*EffectiveExecutionPolicy){
		"role":            func(p *EffectiveExecutionPolicy) { p.Role = RoleForegroundPersonal },
		"namespace":       func(p *EffectiveExecutionPolicy) { p.Scope.NamespaceID = "other" },
		"epoch":           func(p *EffectiveExecutionPolicy) { p.Scope.NamespaceEpoch = 2 },
		"scope revision":  func(p *EffectiveExecutionPolicy) { p.Scope.RegistrationRevision = 2 },
		"policy revision": func(p *EffectiveExecutionPolicy) { p.PolicyRevision = 2 },
		"mount source":    func(p *EffectiveExecutionPolicy) { p.Mounts[0].Source = "/d/other" },
		"mount target":    func(p *EffectiveExecutionPolicy) { p.Mounts[0].Target = "/workspace/x" },
		"mount mode":      func(p *EffectiveExecutionPolicy) { p.Mounts[0].ReadOnly = true },
		"extra mount": func(p *EffectiveExecutionPolicy) {
			p.Mounts = append(p.Mounts, Mount{Source: "/p", Target: "/workspace/project"})
		},
		"env name":      func(p *EffectiveExecutionPolicy) { p.EnvNames = append(p.EnvNames, "ANTHROPIC_API_KEY") },
		"cred provider": func(p *EffectiveExecutionPolicy) { p.Credentials.Provider = "codex" },
		"cred kind":     func(p *EffectiveExecutionPolicy) { p.Credentials.Kind = "env" },
		"cred source":   func(p *EffectiveExecutionPolicy) { p.Credentials.Sources = []string{"/d/s2"} },
		"cred shared":   func(p *EffectiveExecutionPolicy) { p.Credentials.SharedWithForeground = true },
		"image":         func(p *EffectiveExecutionPolicy) { p.Image = "img:2" },
		"grant":         func(p *EffectiveExecutionPolicy) { p.Grants = append(p.Grants, "ipc:tasks") },
	} {
		p := basePolicy()
		mut(&p)
		if p.Fingerprint() == base {
			t.Errorf("changing %s did not change the fingerprint", name)
		}
	}
}

func TestFingerprintNeverDependsOnSecretValues(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-one")
	p1, err := m.r.planFor(context.Background(), "owner", RoleForegroundPersonal, m.r.provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_API_KEY", "sk-secret-two")
	p2, err := m.r.planFor(context.Background(), "owner", RoleForegroundPersonal, m.r.provider, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p1.Policy.Fingerprint() != p2.Policy.Fingerprint() {
		t.Fatal("rotating a secret value changed the policy fingerprint")
	}
	for k, v := range p1.Conf.Labels {
		if strings.Contains(v, "secret") || strings.Contains(k, "secret") {
			t.Fatalf("label %s leaks a secret", k)
		}
	}
	if !strings.Contains(strings.Join(p1.Policy.EnvNames, ","), "ANTHROPIC_API_KEY") {
		t.Fatal("foreground policy must list the forwarded env NAME")
	}
}

func TestTaskRoleHasNoOwnerAuthority(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			m := newManagedTestRunner(t, prov, allowShared)
			t.Setenv("OPENAI_API_KEY", "x")
			t.Setenv("ANTHROPIC_API_KEY", "x")
			m.paths.ObsidianVault = t.TempDir()
			m.r.paths = m.paths
			ctx := context.Background()
			fg, err := m.r.planFor(ctx, "owner", RoleForegroundPersonal, prov, nil)
			if err != nil {
				t.Fatal(err)
			}
			task, err := m.r.planFor(ctx, "owner", RoleScheduledTaskPersonal, prov, nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, mt := range task.Mounts {
				switch mt.Target {
				case "/workspace/project", "/workspace/store/store.sqlite", "/workspace/vault", "/home/node/.claude/skills", "/workspace/project/.env":
					t.Errorf("task role mounts owner-only %s", mt.Target)
				}
			}
			if len(task.Policy.EnvNames) != 0 {
				t.Errorf("task role forwards env %v", task.Policy.EnvNames)
			}
			for _, g := range task.Policy.Grants {
				if strings.HasPrefix(g, "owner:") || g == "ipc:tasks" || g == "memory:api" {
					t.Errorf("task role granted %s", g)
				}
			}
			if len(fg.Policy.EnvNames) == 0 {
				t.Error("foreground default must keep its forwarded env names")
			}
			hasProject := false
			for _, mt := range fg.Mounts {
				hasProject = hasProject || mt.Target == "/workspace/project"
			}
			if !hasProject {
				t.Error("foreground default lost its owner project mount")
			}
			if fg.Policy.Fingerprint() == task.Policy.Fingerprint() {
				t.Error("roles share a fingerprint")
			}
			if !task.Policy.Credentials.SharedWithForeground {
				t.Error("shared native state dir must be flagged on the task binding")
			}
			if task.Conf.Labels[LabelPolicyRole] != string(RoleScheduledTaskPersonal) ||
				task.Conf.Labels[LabelPolicyFingerprint] != task.Policy.Fingerprint() ||
				task.Conf.Labels[LabelPolicyConfig] == "" {
				t.Errorf("labels = %v", task.Conf.Labels)
			}
		})
	}
}

func TestTaskRoleDeniedWithoutQualifiedCredentials(t *testing.T) {
	for _, prov := range providersUnderTest() {
		m := newManagedTestRunner(t, prov, nil)
		_, err := m.r.planFor(context.Background(), "owner", RoleScheduledTaskPersonal, prov, nil)
		if !errors.Is(err, ErrTaskRoleUnqualified) {
			t.Errorf("%s: unqualified task role must be denied, got %v", prov.Name(), err)
		}
		if _, err := m.r.ResolveTaskSnapshot(context.Background(), "owner"); !errors.Is(err, ErrTaskDenied) {
			t.Errorf("%s: snapshot must be denied, got %v", prov.Name(), err)
		}
		// The foreground default is unaffected by the missing qualifier.
		if _, err := m.r.planFor(context.Background(), "owner", RoleForegroundPersonal, prov, nil); err != nil {
			t.Errorf("%s: foreground plan: %v", prov.Name(), err)
		}
		deny := func(string, CredentialBinding) error { return errors.New("no") }
		m2 := newManagedTestRunner(t, prov, deny)
		if _, err := m2.r.planFor(context.Background(), "owner", RoleScheduledTaskPersonal, prov, nil); !errors.Is(err, ErrTaskRoleUnqualified) {
			t.Errorf("%s: qualifier denial must deny, got %v", prov.Name(), err)
		}
	}
}

func TestPlanFailsClosedWithoutScope(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	m.scopes.err = store.ErrStaleScope
	_, err := m.r.planFor(context.Background(), "owner", RoleForegroundPersonal, m.r.provider, nil)
	if !errors.Is(err, ErrPolicyUnknown) {
		t.Fatalf("err = %v", err)
	}
}
