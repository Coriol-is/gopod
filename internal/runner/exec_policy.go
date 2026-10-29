package runner

// Effective execution policy (CP-004 plan PR 3, D044).
//
// A container is reused only when the policy it was created under is the policy
// the next run asks for. The policy is derived by the host from its own state
// (role, immutable scope revision, resolved mounts, env allowlist NAMES, the way
// provider credentials reach the container, image reference and declared
// grants); nothing here is read from the model, the folder name or a mutable
// workspace file. The fingerprint is a hash of that structure: it never covers
// secret values and is safe to log, store on an attempt and put in a label.
//
// This is bounded runtime authority checking. It does not prove filesystem or
// native-account isolation.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"

	"github.com/Coriol-is/gopod/internal/store"
)

// ExecRole is the host-assigned purpose of a container run.
type ExecRole string

const (
	// RoleForegroundPersonal is the existing personal default: owner tier mounts,
	// env and tools, exactly what Run/RunStream have always used.
	RoleForegroundPersonal ExecRole = "foreground_personal"
	// RoleScheduledTaskPersonal is the managed task role: registered-tier mounts,
	// no forwarded env, and only the grants a scheduled run needs. A private
	// owner source does not promote a task to owner mounts, credentials or tools.
	RoleScheduledTaskPersonal ExecRole = "scheduled_task_personal"
)

// Valid reports whether r is a role this host knows.
func (r ExecRole) Valid() bool {
	return r == RoleForegroundPersonal || r == RoleScheduledTaskPersonal
}

// policyRevision is bumped whenever a role definition changes, so containers
// created under an older definition are recycled rather than trusted.
const policyRevision = 1

// fingerprintPrefix versions the fingerprint encoding.
const fingerprintPrefix = "epp1:"

// Container labels carrying the creation policy.
const (
	LabelPolicyRole        = "gopod.policy.role"
	LabelPolicyFingerprint = "gopod.policy.fingerprint"
	LabelPolicyConfig      = "gopod.policy.config"
	LabelPolicyVersion     = "gopod.policy.v"
)

// Grants are host-declared capability names. They are part of the fingerprint
// so a change of grant recycles the container; the IPC watcher and memory API
// still enforce authority from the host-controlled namespace, not from here.
var roleGrants = map[ExecRole][]string{
	RoleForegroundPersonal: {
		"ipc:artifacts", "ipc:messages", "ipc:tasks", "memory:api", "owner:project-ro", "owner:store-rw",
	},
	RoleScheduledTaskPersonal: {
		"ipc:messages", "ipc:task-results",
	},
}

// CredentialBinding describes how provider credentials reach the container.
type CredentialBinding struct {
	Provider string
	// Kind is "native_state_dir": the CLI's login state lives in a host-backed
	// directory mounted into the container. Today that directory also holds the
	// foreground transcripts, so a second role sharing it exposes that state.
	Kind string
	// Sources are the host directories that carry the state, sorted.
	Sources []string
	// SharedWithForeground is true when the foreground role mounts the same
	// sources. A task role with a shared binding needs an explicit qualifier.
	SharedWithForeground bool
}

const credNativeStateDir = "native_state_dir"

// EffectiveExecutionPolicy is the host-derived authority of one container run.
type EffectiveExecutionPolicy struct {
	Role           ExecRole
	Scope          store.ScopeRef
	PolicyRevision int
	Mounts         []Mount
	EnvNames       []string // allowlisted names only, never values
	Credentials    CredentialBinding
	Image          string // image reference; the immutable ID is verified at ensure
	Grants         []string
}

type canonMount struct {
	Source string `json:"s"`
	Target string `json:"t"`
	RO     bool   `json:"ro"`
}

type canonPolicy struct {
	V       int          `json:"v"`
	Role    string       `json:"role"`
	NS      string       `json:"ns"`
	Epoch   int64        `json:"epoch"`
	RegRev  int64        `json:"regrev"`
	Folder  string       `json:"folder"`
	PolRev  int          `json:"polrev"`
	Mounts  []canonMount `json:"mounts"`
	Env     []string     `json:"env"`
	CredPrv string       `json:"credprov"`
	CredKnd string       `json:"credkind"`
	CredSrc []string     `json:"credsrc"`
	CredShr bool         `json:"credshared"`
	Image   string       `json:"image"`
	Grants  []string     `json:"grants"`
}

func canonMounts(in []Mount) []canonMount {
	out := make([]canonMount, len(in))
	for i, m := range in {
		out[i] = canonMount{Source: m.Source, Target: m.Target, RO: m.ReadOnly}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Target != out[j].Target {
			return out[i].Target < out[j].Target
		}
		if out[i].Source != out[j].Source {
			return out[i].Source < out[j].Source
		}
		return !out[i].RO && out[j].RO
	})
	return out
}

func sortedUnique(in []string) []string {
	out := slices.Clone(in)
	sort.Strings(out)
	return slices.Compact(out)
}

// Fingerprint is the deterministic identity of the policy. Equal policies give
// equal fingerprints regardless of input ordering; any change of role, scope
// revision, mount source/target/mode, env name, credential binding, image
// reference or grant changes it.
func (p EffectiveExecutionPolicy) Fingerprint() string {
	c := canonPolicy{
		V:       1,
		Role:    string(p.Role),
		NS:      p.Scope.NamespaceID,
		Epoch:   p.Scope.NamespaceEpoch,
		RegRev:  p.Scope.RegistrationRevision,
		Folder:  p.Scope.Folder,
		PolRev:  p.PolicyRevision,
		Mounts:  canonMounts(p.Mounts),
		Env:     sortedUnique(p.EnvNames),
		CredPrv: p.Credentials.Provider,
		CredKnd: p.Credentials.Kind,
		CredSrc: sortedUnique(p.Credentials.Sources),
		CredShr: p.Credentials.SharedWithForeground,
		Image:   p.Image,
		Grants:  sortedUnique(p.Grants),
	}
	b, err := json.Marshal(c)
	if err != nil { // a struct of strings and ints cannot fail to marshal
		panic(fmt.Sprintf("runner: policy fingerprint: %v", err))
	}
	sum := sha256.Sum256(b)
	return fingerprintPrefix + hex.EncodeToString(sum[:])
}

// Labels returns the creation labels for the policy. cfgDigest is the digest of
// the inspectable configuration (see configDigest).
func (p EffectiveExecutionPolicy) Labels(cfgDigest string) map[string]string {
	return map[string]string{
		LabelPolicyRole:        string(p.Role),
		LabelPolicyFingerprint: p.Fingerprint(),
		LabelPolicyConfig:      cfgDigest,
		LabelPolicyVersion:     fmt.Sprint(p.PolicyRevision),
	}
}

// stateDirSources returns the native credential/session directories among the
// mounts, derived from the mount list itself.
func stateDirSources(mounts []Mount) []string {
	var out []string
	for _, m := range mounts {
		if m.Target == "/home/node/.claude" || m.Target == "/home/node/.codex" {
			out = append(out, m.Source)
		}
	}
	return sortedUnique(out)
}
