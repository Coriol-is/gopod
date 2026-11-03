package runner

import (
	"context"
	"errors"
	"testing"
)

func goodElevatedConfig() ElevatedSpawnConfig {
	return ElevatedSpawnConfig{Image: "elevated:test", Version: "test", Provider: "claude", WorkspaceDir: "/chats/elevated", ClaudeDir: "/data/elevated/.claude", CodexDir: "/data/elevated/.codex", MemoryBytes: 1 << 30, NanoCPUs: 1e9, PidsLimit: 128}
}

func TestElevatedProfileContract(t *testing.T) {
	p, err := elevatedPlan(goodElevatedConfig())
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "gopod-elevated" || p.Conf.User != "0:0" || p.Conf.WorkingDir != "/workspace/session" {
		t.Fatalf("identity: %+v", p.Conf)
	}
	h := p.Host
	if !h.Privileged || h.ReadonlyRootfs || h.PidMode != "host" || h.NetworkMode != "host" || h.IpcMode != "host" {
		t.Fatalf("host authority: %+v", h)
	}
	if len(h.CapDrop) != 0 || len(h.SecurityOpt) != 0 || len(h.ExtraHosts) != 0 || len(h.Tmpfs) != 0 || h.RestartPolicy.Name != "no" {
		t.Fatal("unexpected ordinary profile options")
	}
	if h.Memory <= 0 || h.NanoCPUs <= 0 || h.PidsLimit == nil || *h.PidsLimit <= 0 {
		t.Fatal("missing resource bounds")
	}
	if len(h.Mounts) != 5 || len(p.Conf.Env) != 2 {
		t.Fatal("unexpected mount/env authority")
	}
	if p.Policy.Role != RoleElevatedHost || p.Policy.Scope.Folder != ElevatedFolder || p.Policy.Scope.NamespaceID != "" || p.Policy.Credentials.SharedWithForeground {
		t.Fatal("invalid reserved policy")
	}
	if policyRevision != 1 {
		t.Fatal("existing policy version changed")
	}
}

func TestElevatedProfileRejectsInvalidConfiguration(t *testing.T) {
	for name, change := range map[string]func(*ElevatedSpawnConfig){
		"provider": func(c *ElevatedSpawnConfig) { c.Provider = "unknown" },
		"image":    func(c *ElevatedSpawnConfig) { c.Image = "" },
		"version":  func(c *ElevatedSpawnConfig) { c.Version = "" },
		"relative": func(c *ElevatedSpawnConfig) { c.WorkspaceDir = "relative" },
		"root":     func(c *ElevatedSpawnConfig) { c.ClaudeDir = "/" },
		"alias":    func(c *ElevatedSpawnConfig) { c.CodexDir = c.ClaudeDir },
		"memory":   func(c *ElevatedSpawnConfig) { c.MemoryBytes = 0 },
		"cpu":      func(c *ElevatedSpawnConfig) { c.NanoCPUs = 0 },
		"pids":     func(c *ElevatedSpawnConfig) { c.PidsLimit = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			c := goodElevatedConfig()
			change(&c)
			if _, _, _, err := BuildElevatedContainerArgs(c); err == nil {
				t.Fatal("accepted invalid configuration")
			}
		})
	}
}

func TestElevatedEnsureIndependentAndFailClosed(t *testing.T) {
	c := goodElevatedConfig()
	e := newFakeEngine()
	ctx := context.Background()
	id, err := ensureElevatedContainer(ctx, e, c)
	if err != nil {
		t.Fatal(err)
	}
	same, err := ensureElevatedContainer(ctx, e, c)
	if err != nil || id != same {
		t.Fatalf("reuse: %q %v", same, err)
	}
	for name, change := range map[string]func(*ContainerFacts){
		"pid":        func(f *ContainerFacts) { f.PidMode = "" },
		"network":    func(f *ContainerFacts) { f.NetworkMode = "bridge" },
		"ipc":        func(f *ContainerFacts) { f.IpcMode = "private" },
		"role":       func(f *ContainerFacts) { f.Labels[LabelPolicyRole] = string(RoleForegroundPersonal) },
		"stopped":    func(f *ContainerFacts) { f.Running = false },
		"inspection": func(f *ContainerFacts) { f.Inspected = false },
	} {
		t.Run(name, func(t *testing.T) {
			e := newFakeEngine()
			if _, err := ensureElevatedContainer(ctx, e, c); err != nil {
				t.Fatal(err)
			}
			change(e.get("gopod-elevated"))
			if _, err := ensureElevatedContainer(ctx, e, c); !errors.Is(err, ErrPolicyUnknown) {
				t.Fatalf("expected denial, got %v", err)
			}
			if len(e.opsSnapshot()) != 1 {
				t.Fatal("drift/death recycled container")
			}
		})
	}
}

func TestOrdinaryDigestNormalizesDockerDefaults(t *testing.T) {
	c := goodSpawnConfig()
	conf, host, _, err := BuildContainerArgs(c)
	if err != nil {
		t.Fatal(err)
	}
	f := &ContainerFacts{User: conf.User, ReadonlyRootfs: host.ReadonlyRootfs, Privileged: host.Privileged, CapDrop: host.CapDrop, SecurityOpt: host.SecurityOpt, NetworkMode: "bridge", IpcMode: "private"}
	if digestOfFacts(f, nil) != digestOfSpawn(conf, host, c.Mounts, nil) {
		t.Fatal("Docker defaults changed ordinary digest")
	}
	f.NetworkMode = "host"
	if digestOfFacts(f, nil) == digestOfSpawn(conf, host, c.Mounts, nil) {
		t.Fatal("host network not pinned")
	}
}

func TestManagedPlanRejectsElevatedRole(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.r.planFor(context.Background(), "owner", RoleElevatedHost, m.r.provider, nil); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("ordinary role planner accepted elevated: %v", err)
	}
}

func TestElevatedRejectsSelfConsistentWrongConfiguration(t *testing.T) {
	c := goodElevatedConfig()
	e := newFakeEngine()
	ctx := context.Background()
	if _, err := ensureElevatedContainer(ctx, e, c); err != nil {
		t.Fatal(err)
	}
	f := e.get("gopod-elevated")
	f.NetworkMode = "bridge"
	f.Labels[LabelPolicyConfig] = digestOfFacts(f, nil)
	if _, err := ensureElevatedContainer(ctx, e, c); !errors.Is(err, ErrPolicyUnknown) {
		t.Fatalf("wrong requested configuration accepted: %v", err)
	}
}

func TestNamespaceDigestPinsNonDefaultModes(t *testing.T) {
	base := &ContainerFacts{User: "501:20", ReadonlyRootfs: true, CapDrop: []string{"ALL"}}
	original := digestOfFacts(base, nil)
	for _, mode := range []string{"host", "container:abc", "custom", "none", "shareable"} {
		for _, field := range []string{"pid", "network", "ipc"} {
			t.Run(field+"/"+mode, func(t *testing.T) {
				f := *base
				switch field {
				case "pid":
					f.PidMode = mode
				case "network":
					f.NetworkMode = mode
				case "ipc":
					f.IpcMode = mode
				}
				if digestOfFacts(&f, nil) == original {
					t.Fatal("non-default namespace ignored")
				}
			})
		}
	}
}

func TestElevatedRoleBoundaryIncludesStoppedAndUnlabelled(t *testing.T) {
	p, err := elevatedPlan(goodElevatedConfig())
	if err != nil {
		t.Fatal(err)
	}
	for _, running := range []bool{false, true} {
		for _, role := range []ExecRole{RoleForegroundPersonal, RoleScheduledTaskPersonal, ""} {
			f := &ContainerFacts{Running: running, Labels: map[string]string{LabelPolicyRole: string(role)}}
			if got := decideContainer(p, f, "image", nil); got.Action != actDeny {
				t.Fatalf("incoming %s running=%v: %+v", role, running, got)
			}
			ordinary := &policyPlan{Policy: EffectiveExecutionPolicy{Role: role}}
			f.Labels[LabelPolicyRole] = string(RoleElevatedHost)
			if got := decideContainer(ordinary, f, "image", nil); got.Action != actDeny {
				t.Fatalf("outgoing %s running=%v: %+v", role, running, got)
			}
		}
	}
}
