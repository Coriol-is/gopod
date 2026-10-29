package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"time"

	"github.com/docker/docker/api/types/container"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

// Managed task runtime (CP-004 plan PR 3, D044). Everything here is dormant
// unless the host calls SetManagedTasks: with no managed state the runner takes
// the legacy paths unchanged.

// Errors of the managed task path. All are pre-spawn denials: the caller must
// not report them as "started".
var (
	// ErrManagedTasksDisabled: managed tasks are not enabled on this runner.
	ErrManagedTasksDisabled = errors.New("runner: managed tasks are not enabled")
	// ErrTaskDenied: the host refuses to run this task now (profile, policy,
	// provider or role). Deterministic: retrying the same attempt cannot help.
	ErrTaskDenied = errors.New("runner: managed task denied")
	// ErrTaskRoleUnqualified: no qualified credential adapter supports the task
	// role for this provider, so the provider/task role stays disabled.
	ErrTaskRoleUnqualified = errors.New("runner: task role is not qualified for this provider credential binding")
)

// ScopeSource supplies the host scope snapshot (satisfied by *store.Store).
type ScopeSource interface {
	SnapshotScope(ctx context.Context, folder string) (store.ScopeRef, error)
}

// TaskRoleQualifier decides whether a provider credential binding may serve the
// scheduled task role. It is the only way a task role can run: nil denies all.
// A qualifier must not approve a binding merely to pass a smoke test.
type TaskRoleQualifier func(provider string, b CredentialBinding) error

// ManagedTaskConfig turns the managed task runtime on.
type ManagedTaskConfig struct {
	// Scopes supplies the immutable scope revision folded into every policy.
	Scopes ScopeSource
	// Holds reports whether a possibly running attempt (execution_unknown
	// reservation) holds the folder. Required: without it no recycle is allowed.
	Holds func(folder string) bool
	// Qualifier approves credential bindings for the task role. Nil denies.
	Qualifier TaskRoleQualifier
	// Providers resolves a snapshot's provider name to a provider.
	Providers map[string]AgentProvider
}

type managedState struct {
	scopes    ScopeSource
	holds     func(string) bool
	qualify   TaskRoleQualifier
	providers map[string]AgentProvider
	engine    containerEngine
	exitWait  time.Duration // test hook; 0 = default
}

// SetManagedTasks enables the managed task runtime. Call at startup, before
// the first run. It also routes the foreground container through the policy
// guard, so a task cannot leave a task-role container behind for a foreground
// turn (and vice versa).
func (r *Runner) SetManagedTasks(cfg ManagedTaskConfig) error {
	if cfg.Scopes == nil {
		return errors.New("runner: managed tasks need a scope source")
	}
	m := &managedState{scopes: cfg.Scopes, holds: cfg.Holds, qualify: cfg.Qualifier, providers: cfg.Providers}
	if r.d != nil {
		m.engine = r.d
	}
	r.managed = m
	return nil
}

// universe is the set of env names that may carry forwarded credentials. The
// guard only compares those names, never the image's own environment.
func (m *managedState) universe(r *Runner) map[string]bool {
	u := map[string]bool{}
	add := func(p AgentProvider) {
		if p == nil {
			return
		}
		for _, n := range p.RequiredEnvVars() {
			u[n] = true
		}
	}
	add(r.provider)
	r.providersMu.RLock()
	for _, p := range r.chatProviders {
		add(p)
	}
	r.providersMu.RUnlock()
	for _, p := range m.providers {
		add(p)
	}
	for _, n := range r.allow {
		u[n] = true
	}
	return u
}

// policyPlan is a policy together with the exact create arguments for it.
type policyPlan struct {
	Policy EffectiveExecutionPolicy
	Conf   *container.Config
	Host   *container.HostConfig
	Name   string
	Mounts []Mount
}

// buildSpawn assembles the spawn configuration shared by every path.
func (r *Runner) buildSpawn(folder string, prov AgentProvider, mounts []Mount, envAllow []string) SpawnConfig {
	return SpawnConfig{
		Image:        prov.Image(),
		ChatFolder:   folder,
		Version:      r.version,
		Mounts:       mounts,
		UID:          r.cfg.UID,
		GID:          r.cfg.GID,
		MemoryBytes:  r.cfg.MemoryBytes,
		NanoCPUs:     r.cfg.NanoCPUs,
		PidsLimit:    r.cfg.PidsLimit,
		EnvAllowlist: envAllow,
		HomeDir:      prov.HomeDir(),
	}
}

// planFor derives the policy and create arguments of role for folder. It reads
// the host scope snapshot and nothing the model produced.
func (r *Runner) planFor(ctx context.Context, folder string, role ExecRole, prov AgentProvider, al *mountsec.Allowlist) (*policyPlan, error) {
	m := r.managed
	if m == nil {
		return nil, ErrManagedTasksDisabled
	}
	if !role.Valid() {
		return nil, fmt.Errorf("%w: unknown role", ErrTaskDenied)
	}
	scope, err := m.scopes.SnapshotScope(ctx, folder)
	if err != nil {
		return nil, fmt.Errorf("%w: scope: %v", ErrPolicyUnknown, err)
	}
	tier, extras := TierOwner, al
	if role == RoleScheduledTaskPersonal {
		tier, extras = TierRegistered, nil
	}
	mounts, err := BuildMounts(r.paths, folder, tier, extras)
	if err != nil {
		return nil, err
	}
	if role == RoleScheduledTaskPersonal {
		mounts = taskIPCMounts(mounts, r.paths.DataDir, folder)
	}
	envAllow := sortedUnique(containerEnvAllow(tier, prov, r.allow))

	cred := CredentialBinding{Provider: prov.Name(), Kind: credNativeStateDir, Sources: stateDirSources(mounts)}
	if role == RoleScheduledTaskPersonal {
		fg := stateDirSources(standardMounts(r.paths, folder, TierOwner))
		cred.SharedWithForeground = slices.Equal(cred.Sources, fg)
		if len(cred.Sources) == 0 {
			return nil, fmt.Errorf("%w: no credential binding", ErrTaskRoleUnqualified)
		}
		if m.qualify == nil {
			return nil, ErrTaskRoleUnqualified
		}
		if err := m.qualify(prov.Name(), cred); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrTaskRoleUnqualified, err)
		}
	}
	pol := EffectiveExecutionPolicy{
		Role: role, Scope: scope, PolicyRevision: policyRevision,
		Mounts: mounts, EnvNames: envAllow, Credentials: cred,
		Image: prov.Image(), Grants: roleGrants[role],
	}
	conf, host, name, err := BuildContainerArgs(r.buildSpawn(folder, prov, mounts, envAllow))
	if err != nil {
		return nil, err
	}
	digest := digestOfSpawn(conf, host, mounts, m.universe(r))
	for k, v := range pol.Labels(digest) {
		conf.Labels[k] = v
	}
	return &policyPlan{Policy: pol, Conf: conf, Host: host, Name: name, Mounts: mounts}, nil
}

// taskIPCMounts narrows the task role's IPC access to what its grants name:
// the messages channel and the task-results channel. The whole namespace
// directory (tasks/, results/, input/) is not mounted.
func taskIPCMounts(mounts []Mount, dataDir, folder string) []Mount {
	ipc := filepath.Join(dataDir, "ipc", folder)
	out := make([]Mount, 0, len(mounts)+1)
	for _, m := range mounts {
		if m.Target == "/workspace/ipc" {
			continue
		}
		out = append(out, m)
	}
	return append(out,
		Mount{Source: filepath.Join(ipc, "messages"), Target: "/workspace/ipc/messages"},
		Mount{Source: filepath.Join(ipc, taskResultsDir), Target: "/workspace/ipc/" + taskResultsDir},
	)
}

// taskResultsDir is the report channel directory (taskreport.DirName).
const taskResultsDir = "task-results"

// ensureRole is the guarded Ensure: lock, authorize, plan, guard. The caller
// of a run says so explicitly with slotHeld (it is not inferred from the slot
// table: another run may hold it). Only such a caller may recycle a running
// container. A non-empty expectFingerprint must
// equal the planned policy's fingerprint (the attempt's frozen profile).
func (r *Runner) ensureRole(ctx context.Context, folder string, role ExecRole, prov AgentProvider, al *mountsec.Allowlist, reconcile, slotHeld bool, expectFingerprint string) (string, error) {
	if folder == "" {
		return "", errors.New("runner: empty chatFolder")
	}
	r.lockChat(folder)
	defer r.unlockChat(folder)
	if err := r.authorizeProfile(ctx, folder); err != nil {
		return "", err
	}
	if err := EnsureChatDirs(r.paths, folder, role == RoleForegroundPersonal, r.log); err != nil {
		return "", err
	}
	if role == RoleScheduledTaskPersonal {
		if err := ensureReportDir(filepath.Join(r.paths.DataDir, "ipc", folder, taskResultsDir)); err != nil {
			return "", err
		}
	}
	plan, err := r.planFor(ctx, folder, role, prov, al)
	if err != nil {
		return "", err
	}
	if expectFingerprint != "" && plan.Policy.Fingerprint() != expectFingerprint {
		// The scope revision, mounts, image or grants moved since the attempt
		// froze its profile: the host no longer vouches for that authority.
		return "", fmt.Errorf("%w: execution policy changed since the attempt snapshot", ErrTaskDenied)
	}
	return r.ensureGuarded(ctx, plan, slotHeld, reconcile)
}
