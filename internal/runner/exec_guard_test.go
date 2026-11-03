package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

// ensure runs ensureRole the way a run does: holding the folder's agent slot.
func (m *managedTestRunner) ensure(t *testing.T, role ExecRole) (string, error) {
	t.Helper()
	release, err := m.r.acquireAgentSlot(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	return m.r.ensureRole(context.Background(), "owner", role, m.r.provider, nil, false, true, "")
}

func TestGuardReusesEqualPolicy(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			m := newManagedTestRunner(t, prov, allowShared)
			id1, err := m.ensure(t, RoleForegroundPersonal)
			if err != nil {
				t.Fatal(err)
			}
			id2, err := m.ensure(t, RoleForegroundPersonal)
			if err != nil || id1 != id2 {
				t.Fatalf("reuse: %v %q %q", err, id1, id2)
			}
			if got := m.eng.opsSnapshot(); !slices.Equal(got, []string{"create:foreground_personal"}) {
				t.Fatalf("ops = %v", got)
			}
		})
	}
}

// Owner foreground -> task -> foreground: the container is recycled twice, the
// host-backed native state and session identifiers are never touched, and the
// final foreground container is exactly the original default.
func TestGuardRecyclesBetweenRolesAndPreservesNativeState(t *testing.T) {
	for _, prov := range providersUnderTest() {
		t.Run(prov.Name(), func(t *testing.T) {
			m := newManagedTestRunner(t, prov, allowShared)
			if err := EnsureChatDirs(m.paths, "owner", true, nil); err != nil {
				t.Fatal(err)
			}
			claudeDir := filepath.Join(m.paths.DataDir, "sessions", "owner", ".claude")
			codexDir := filepath.Join(m.paths.DataDir, "sessions", "owner", ".codex")
			const sessionID = "0b8a52c8-5d0e-4a77-9a39-0d6f6d0a4e11"
			mustWrite(t, filepath.Join(claudeDir, "projects", "p", sessionID+".jsonl"), "{}")
			mustWrite(t, filepath.Join(claudeDir, ".credentials.json"), "cred-claude")
			mustWrite(t, filepath.Join(codexDir, "auth.json"), "cred-codex")
			mustWrite(t, filepath.Join(codexDir, "sessions", "rollout-"+sessionID+".jsonl"), "{}")

			if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
				t.Fatal(err)
			}
			fgBefore := m.eng.get("gopod-owner")
			fgLabels := map[string]string{}
			for k, v := range fgBefore.Labels {
				fgLabels[k] = v
			}
			if _, err := m.ensure(t, RoleScheduledTaskPersonal); err != nil {
				t.Fatal(err)
			}
			task := m.eng.get("gopod-owner")
			if task.Labels[LabelPolicyRole] != string(RoleScheduledTaskPersonal) {
				t.Fatalf("task container role = %q", task.Labels[LabelPolicyRole])
			}
			for _, mt := range task.Mounts {
				if mt.Target == "/workspace/project" || mt.Target == "/workspace/store/store.sqlite" {
					t.Fatalf("task container mounts owner-only %s", mt.Target)
				}
			}
			if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
				t.Fatal(err)
			}
			fgAfter := m.eng.get("gopod-owner")
			for _, k := range []string{LabelPolicyRole, LabelPolicyFingerprint, LabelPolicyConfig} {
				if fgAfter.Labels[k] != fgLabels[k] {
					t.Errorf("foreground label %s changed after the role round trip", k)
				}
			}
			if len(fgAfter.Mounts) != len(fgBefore.Mounts) {
				t.Errorf("foreground mounts %d -> %d", len(fgBefore.Mounts), len(fgAfter.Mounts))
			}
			want := []string{"create:foreground_personal", "stop_remove:c1", "create:scheduled_task_personal", "stop_remove:c2", "create:foreground_personal"}
			if got := m.eng.opsSnapshot(); !slices.Equal(got, want) {
				t.Fatalf("ops = %v, want %v", got, want)
			}
			for path, content := range map[string]string{
				filepath.Join(claudeDir, ".credentials.json"):                      "cred-claude",
				filepath.Join(codexDir, "auth.json"):                               "cred-codex",
				filepath.Join(claudeDir, "projects", "p", sessionID+".jsonl"):      "{}",
				filepath.Join(codexDir, "sessions", "rollout-"+sessionID+".jsonl"): "{}",
			} {
				b, err := os.ReadFile(path)
				if err != nil || string(b) != content {
					t.Errorf("native state %s lost or changed: %v", filepath.Base(path), err)
				}
			}
		})
	}
}

func TestGuardRecyclesOnScopeRevisionChange(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	m.scopes.bump("owner")
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	if got := m.eng.opsSnapshot(); len(got) != 3 || got[1] != "stop_remove:c1" {
		t.Fatalf("ops = %v", got)
	}
}

func TestGuardFailsClosedOnUntrustedContainer(t *testing.T) {
	for name, tamper := range map[string]func(f *ContainerFacts){
		"partial labels":      func(f *ContainerFacts) { f.Labels = map[string]string{LabelPolicyRole: "foreground_personal"} },
		"missing fingerprint": func(f *ContainerFacts) { delete(f.Labels, LabelPolicyFingerprint) },
		"unknown role":        func(f *ContainerFacts) { f.Labels[LabelPolicyRole] = "owner" },
		"stale version":       func(f *ContainerFacts) { f.Labels[LabelPolicyVersion] = "0" },
		"unsupported inspect": func(f *ContainerFacts) { f.Inspected = false },
		"extra mount": func(f *ContainerFacts) {
			f.Mounts = append(f.Mounts, Mount{Source: "/etc", Target: "/workspace/project"})
		},
		"widened mode": func(f *ContainerFacts) { f.Mounts[0].ReadOnly = !f.Mounts[0].ReadOnly },
		"extra credential env": func(f *ContainerFacts) {
			f.EnvNames = append(f.EnvNames, "FREEFEED_BASE_URL")
		},
		"privileged":  func(f *ContainerFacts) { f.Privileged = true },
		"caps back":   func(f *ContainerFacts) { f.CapDrop = nil },
		"writable fs": func(f *ContainerFacts) { f.ReadonlyRootfs = false },
		"other user":  func(f *ContainerFacts) { f.User = "0:0" },
	} {
		t.Run(name, func(t *testing.T) {
			m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
			if _, err := m.ensure(t, RoleScheduledTaskPersonal); err != nil {
				t.Fatal(err)
			}
			tamper(m.eng.get("gopod-owner"))
			for _, role := range []ExecRole{RoleScheduledTaskPersonal, RoleForegroundPersonal} {
				_, err := m.ensure(t, role)
				if !errors.Is(err, ErrPolicyUnknown) {
					t.Fatalf("%s: err = %v, want ErrPolicyUnknown", role, err)
				}
			}
			if got := m.eng.opsSnapshot(); len(got) != 1 {
				t.Fatalf("an untrusted container was touched: %v", got)
			}
		})
	}
}

func TestGuardFailsClosedWhenInspectionFails(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	m.eng.inspectErr = errors.New("daemon down")
	if _, err := m.ensure(t, RoleForegroundPersonal); !errors.Is(err, ErrPolicyUnknown) {
		t.Fatalf("err = %v", err)
	}
	if len(m.eng.opsSnapshot()) != 0 {
		t.Fatal("created a container without inspection")
	}
}

// A possibly running attempt (execution_unknown reservation) blocks recycling:
// the container it may be running in is never stopped.
func TestGuardNeverRecyclesUnderUnknownExecution(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.ensure(t, RoleScheduledTaskPersonal); err != nil {
		t.Fatal(err)
	}
	m.holds["owner"] = true
	for _, role := range []ExecRole{RoleForegroundPersonal} {
		if _, err := m.ensure(t, role); !errors.Is(err, ErrExecutionUnproven) {
			t.Fatalf("%s: err = %v", role, err)
		}
	}
	if got := m.eng.opsSnapshot(); len(got) != 1 {
		t.Fatalf("recycled under an unproven execution: %v", got)
	}
	// Equal policy is still reusable (nothing is recycled).
	if _, err := m.ensure(t, RoleScheduledTaskPersonal); err != nil {
		t.Fatalf("reuse under hold: %v", err)
	}
	// Released: recycle proceeds.
	m.holds["owner"] = false
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	// No hold source configured: fail closed.
	m.r.managed.holds = nil
	if _, err := m.ensure(t, RoleScheduledTaskPersonal); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatalf("no hold source: err = %v", err)
	}
}

func TestGuardRecycleRequiresTheFolderSlot(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.ensure(t, RoleScheduledTaskPersonal); err != nil {
		t.Fatal(err)
	}
	// Called without holding the slot (a bare Ensure): reuse works, recycle does not.
	if _, err := m.r.ensureRole(context.Background(), "owner", RoleScheduledTaskPersonal, m.r.provider, nil, false, false, ""); err != nil {
		t.Fatalf("reuse without slot: %v", err)
	}
	if _, err := m.r.ensureRole(context.Background(), "owner", RoleForegroundPersonal, m.r.provider, nil, false, false, ""); !errors.Is(err, ErrRecycleNeedsSlot) {
		t.Fatalf("err = %v", err)
	}
}

func TestGuardStoppedContainerIsReplacedAndImageMismatchPreserved(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	m.eng.get("gopod-owner").Running = false
	m.holds["owner"] = true // irrelevant: nothing runs in a stopped container
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	m.eng.images["img"] = "sha256:newer"
	if _, err := m.ensure(t, RoleForegroundPersonal); err == nil {
		t.Fatal("image drift must not recycle outside reconcile")
	} else if _, ok := err.(*ImageMismatchError); !ok {
		t.Fatalf("err = %v", err)
	}
}

func TestGuardExpectedFingerprintMismatchDeniesTask(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	snap, err := m.r.ResolveTaskSnapshot(context.Background(), "owner")
	if err != nil {
		t.Fatal(err)
	}
	release, _ := m.r.acquireAgentSlot(context.Background(), "owner")
	defer release()
	if _, err := m.r.ensureRole(context.Background(), "owner", RoleScheduledTaskPersonal, m.r.provider, nil, false, true, snap.ProfileFingerprint); err != nil {
		t.Fatal(err)
	}
	m.scopes.bump("owner")
	if _, err := m.r.ensureRole(context.Background(), "owner", RoleScheduledTaskPersonal, m.r.provider, nil, false, true, snap.ProfileFingerprint); !errors.Is(err, ErrTaskDenied) {
		t.Fatalf("err = %v", err)
	}
}

func TestLegacyRunnerIsUntouchedWithoutManagedRuntime(t *testing.T) {
	r := &Runner{profilePolicy: testPersonalPolicy(), provider: NewClaudeProvider("img")}
	if r.managed != nil {
		t.Fatal("managed runtime must be off by default")
	}
	if _, err := r.RunTaskStream(context.Background(), TaskRunRequest{}); !errors.Is(err, ErrManagedTasksDisabled) {
		t.Fatalf("err = %v", err)
	}
	if _, err := r.ResolveTaskSnapshot(context.Background(), "owner"); !errors.Is(err, ErrManagedTasksDisabled) {
		t.Fatalf("err = %v", err)
	}
}

// The public Ensure (login flow) holds no slot: it never recycles a running
// container, whether a foreground turn is live or the folder is idle, but it
// still creates and reuses as before.
func TestPublicEnsureNeverRecyclesARunningContainer(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	ctx := context.Background()
	// Nothing running: Ensure creates (today's behaviour).
	id1, err := m.r.Ensure(ctx, "owner", TierOwner, nil)
	if err != nil {
		t.Fatal(err)
	}
	if id2, err := m.r.Ensure(ctx, "owner", TierOwner, nil); err != nil || id2 != id1 {
		t.Fatalf("reuse: %v", err)
	}
	// A live foreground turn holds the slot; the scope changes under it.
	release, err := m.r.acquireAgentSlot(ctx, "owner")
	if err != nil {
		t.Fatal(err)
	}
	m.scopes.bump("owner")
	if _, err := m.r.Ensure(ctx, "owner", TierOwner, nil); !errors.Is(err, ErrFolderBusy) {
		t.Fatalf("live turn: err = %v", err)
	}
	release()
	// Idle folder, policy differs: still refused for a slotless caller.
	if _, err := m.r.Ensure(ctx, "owner", TierOwner, nil); !errors.Is(err, ErrRecycleNeedsSlot) {
		t.Fatalf("idle: err = %v", err)
	}
	if got := m.eng.opsSnapshot(); len(got) != 1 {
		t.Fatalf("a slotless caller touched the container: %v", got)
	}
	// The slot-holding path (a turn, startup) does recycle.
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	if got := m.eng.opsSnapshot(); len(got) != 3 {
		t.Fatalf("ops = %v", got)
	}
}

// Internal callers (r.ensure: turns, auth checks, clears) hold the slot and may
// recycle; the explicit flag, not the slot table, decides.
func TestInternalEnsureRecyclesWithSlot(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	ctx := context.Background()
	if _, err := m.r.ensure(ctx, "owner", TierOwner, nil); err != nil {
		t.Fatal(err)
	}
	m.scopes.bump("owner")
	if _, err := m.r.ensure(ctx, "owner", TierOwner, nil); err != nil {
		t.Fatalf("slot-holding caller must recycle: %v", err)
	}
	if got := m.eng.opsSnapshot(); len(got) != 3 {
		t.Fatalf("ops = %v", got)
	}
}

// A container with no policy labels at all pre-dates the guard (for example when
// startup cleanup is disabled). It is never reused; the slot-holding caller may
// replace it when nothing is unproven, a slotless caller may not.
func TestGuardReplacesUnlabeledLegacyContainerOnlyUnderTheSlot(t *testing.T) {
	m := newManagedTestRunner(t, NewClaudeProvider("img"), allowShared)
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	m.eng.get("gopod-owner").Labels = map[string]string{"gopod.chat": "owner", "gopod.version": "test"}
	if _, err := m.r.Ensure(context.Background(), "owner", TierOwner, nil); !errors.Is(err, ErrRecycleNeedsSlot) {
		t.Fatalf("slotless: %v", err)
	}
	m.holds["owner"] = true
	if _, err := m.ensure(t, RoleForegroundPersonal); !errors.Is(err, ErrExecutionUnproven) {
		t.Fatalf("unproven hold: %v", err)
	}
	m.holds["owner"] = false
	if _, err := m.ensure(t, RoleForegroundPersonal); err != nil {
		t.Fatal(err)
	}
	if got := m.eng.opsSnapshot(); len(got) != 3 || got[2] != "create:foreground_personal" {
		t.Fatalf("ops = %v", got)
	}
}

// The only tolerated normalizations of the inspected hardening fields.
func TestHardeningNormalizationIsExactlyTheDocumentedSet(t *testing.T) {
	base := hardeningOf("501:20", true, false, []string{"ALL"}, []string{"no-new-privileges:true"})
	for name, h := range map[string]hardening{
		"cap case":     hardeningOf("501:20", true, false, []string{"all"}, []string{"no-new-privileges:true"}),
		"secopt short": hardeningOf("501:20", true, false, []string{"ALL"}, []string{"no-new-privileges"}),
	} {
		if h != base {
			t.Errorf("%s must be tolerated", name)
		}
	}
	for name, h := range map[string]hardening{
		"user":     hardeningOf("501", true, false, []string{"ALL"}, []string{"no-new-privileges:true"}),
		"no caps":  hardeningOf("501:20", true, false, nil, []string{"no-new-privileges:true"}),
		"no nnp":   hardeningOf("501:20", true, false, []string{"ALL"}, []string{"seccomp=unconfined"}),
		"writable": hardeningOf("501:20", false, false, []string{"ALL"}, []string{"no-new-privileges:true"}),
	} {
		if h == base {
			t.Errorf("%s must NOT be tolerated", name)
		}
	}
}
