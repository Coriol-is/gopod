package runner

// Container reuse guard (CP-004 plan PR 3, D044).
//
// Under the per-folder lock the guard compares three things before it lets a
// container serve a role: the policy the run asks for, the policy recorded on
// the container's labels at creation, and the configuration Docker reports for
// it now. Anything it cannot establish fails closed:
//
//   - labels missing, malformed or from another policy version: untrusted, never
//     reused and never recycled behind the owner's back;
//   - inspection unsupported or incomplete: untrusted;
//   - the inspected configuration differs from the digest recorded at creation
//     (drift or tampering): untrusted.
//
// Only a well-formed, internally consistent container whose recorded policy
// differs from the requested one is a known mismatch. A known mismatch is
// recycled (stop, remove, recreate with the requested policy) but only after the
// caller holds the folder's agent slot, so any execution the runner started has
// exited, and only while no possibly running attempt holds the folder
// (execution_unknown reservations). Host-backed native credentials and session
// directories are bind mounts: recycling never touches them.

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/container"
)

// Guard failures. All of them mean "do not run in this container".
var (
	// ErrPolicyUnknown: the container's recorded or effective policy cannot be
	// established (missing labels, unsupported inspection, config drift).
	ErrPolicyUnknown = errors.New("runner: container policy cannot be established")
	// ErrRecycleNeedsSlot: a recycle was needed but the caller does not hold the
	// folder's agent slot, so a running execution cannot be ruled out.
	ErrRecycleNeedsSlot = errors.New("runner: container recycle requires the folder agent slot")
	// ErrExecutionUnproven: a possibly running attempt holds the folder.
	ErrExecutionUnproven = errors.New("runner: a task attempt with unproven exit holds this folder")
)

// ContainerFacts is what Docker reports about an existing container, reduced to
// what the guard compares. Env holds NAMES only: values never leave Docker.
type ContainerFacts struct {
	ID             string
	Running        bool
	ImageID        string
	Labels         map[string]string
	EnvNames       []string
	Mounts         []Mount // bind mounts, effective mode
	User           string
	ReadonlyRootfs bool
	Privileged     bool
	CapDrop        []string
	SecurityOpt    []string
	// Inspected is false when the engine could not report the full configuration.
	Inspected bool
}

// containerEngine is the slice of Docker the guard needs, declared at the
// consumer so tests can fake it. *Docker implements it (docker_policy.go).
type containerEngine interface {
	// InspectContainer returns nil facts, nil error when no container has the name.
	InspectContainer(ctx context.Context, name string) (*ContainerFacts, error)
	ImageID(ctx context.Context, ref string) (string, error)
	StopRemove(ctx context.Context, id string) error
	CreateStart(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error)
}

// hardening is the inspectable security configuration the guard pins.
type hardening struct {
	User       string `json:"user"`
	ReadonlyFS bool   `json:"rofs"`
	Privileged bool   `json:"priv"`
	DropAll    bool   `json:"dropall"`
	NoNewPrivs bool   `json:"nonewprivs"`
}

// hardeningOf normalizes the inspected security fields. The tolerated
// normalizations are exactly these and nothing else; every other difference
// between the recorded and the inspected configuration is a mismatch and is
// denied (a daemon that rewrites bind sources, User or other fields makes the
// container undeployable until the digest is deliberately extended, which is
// why a live create/inspect/reuse check is required before enabling the flag):
//
//   - CapDrop "ALL" is compared case-insensitively;
//   - SecurityOpt "no-new-privileges" and "no-new-privileges:true" are equal.
func hardeningOf(user string, rofs, priv bool, capDrop, secOpt []string) hardening {
	return hardening{
		User:       user,
		ReadonlyFS: rofs,
		Privileged: priv,
		DropAll:    slices.ContainsFunc(capDrop, func(c string) bool { return strings.EqualFold(c, "ALL") }),
		NoNewPrivs: slices.Contains(secOpt, "no-new-privileges:true") || slices.Contains(secOpt, "no-new-privileges"),
	}
}

// configDigest hashes the configuration Docker can report: bind mounts, the
// env names that belong to the forwarded-credential universe, and hardening.
// The same function runs over the requested spawn and over the inspection, so
// equality means "the container is what was asked for".
func configDigest(mounts []Mount, envNames []string, universe map[string]bool, h hardening) string {
	var env []string
	for _, n := range envNames {
		if universe[n] {
			env = append(env, n)
		}
	}
	b, err := json.Marshal(struct {
		M []canonMount `json:"m"`
		E []string     `json:"e"`
		H hardening    `json:"h"`
	}{canonMounts(mounts), sortedUnique(env), h})
	if err != nil {
		panic(fmt.Sprintf("runner: config digest: %v", err))
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// envNamesOf extracts NAMES from KEY=VALUE entries.
func envNamesOf(env []string) []string {
	out := make([]string, 0, len(env))
	for _, kv := range env {
		name, _, _ := strings.Cut(kv, "=")
		if name != "" {
			out = append(out, name)
		}
	}
	return out
}

// digestOfFacts computes configDigest from an inspection.
func digestOfFacts(f *ContainerFacts, universe map[string]bool) string {
	return configDigest(f.Mounts, f.EnvNames, universe,
		hardeningOf(f.User, f.ReadonlyRootfs, f.Privileged, f.CapDrop, f.SecurityOpt))
}

// digestOfSpawn computes configDigest from the create arguments.
func digestOfSpawn(conf *container.Config, host *container.HostConfig, mounts []Mount, universe map[string]bool) string {
	return configDigest(mounts, envNamesOf(conf.Env), universe,
		hardeningOf(conf.User, host.ReadonlyRootfs, host.Privileged, host.CapDrop, host.SecurityOpt))
}

// hasNoPolicyLabels is true only when none of the policy labels exist. A
// partial or malformed set is corruption, not legacy, and is denied.
func hasNoPolicyLabels(l map[string]string) bool {
	for _, k := range []string{LabelPolicyRole, LabelPolicyFingerprint, LabelPolicyConfig, LabelPolicyVersion} {
		if _, ok := l[k]; ok {
			return false
		}
	}
	return true
}

type guardAction int

const (
	actCreate   guardAction = iota // nothing there
	actReuse                       // consistent and policy equal
	actRecreate                    // consistent, recorded policy differs (or exited)
	actDeny                        // cannot establish: fail closed
)

type guardDecision struct {
	Action guardAction
	Reason string // fixed code, safe to log
	Err    error  // set for actDeny
}

func deny(reason string) guardDecision {
	return guardDecision{Action: actDeny, Reason: reason, Err: fmt.Errorf("%w: %s", ErrPolicyUnknown, reason)}
}

// decideContainer is the pure decision. want is the requested plan, facts the
// inspection (nil = no container), desiredImage the immutable ID of the image
// the plan names.
func decideContainer(want *policyPlan, facts *ContainerFacts, desiredImage string, universe map[string]bool) guardDecision {
	if facts == nil {
		return guardDecision{Action: actCreate, Reason: "absent"}
	}
	if !facts.Running {
		// A stopped container has no execution to protect; the existing rule is
		// to remove it and create fresh with the requested configuration.
		return guardDecision{Action: actRecreate, Reason: "not_running"}
	}
	if !facts.Inspected {
		return deny("inspection_unsupported")
	}
	if hasNoPolicyLabels(facts.Labels) {
		// Created before the guard existed (a legacy foreground container). Its
		// creation policy is unknowable, so it is never reused; it may be replaced,
		// but only by a caller that holds the folder's slot while no unproven
		// attempt holds the folder (the recycle conditions in ensureGuarded).
		return guardDecision{Action: actRecreate, Reason: "unlabeled_legacy"}
	}
	role := ExecRole(facts.Labels[LabelPolicyRole])
	fp := facts.Labels[LabelPolicyFingerprint]
	cfg := facts.Labels[LabelPolicyConfig]
	if !role.Valid() || !strings.HasPrefix(fp, fingerprintPrefix) || cfg == "" {
		return deny("labels_missing")
	}
	if facts.Labels[LabelPolicyVersion] != fmt.Sprint(policyRevision) {
		return deny("labels_stale_version")
	}
	if digestOfFacts(facts, universe) != cfg {
		return deny("inspected_config_differs_from_recorded")
	}
	if fp != want.Policy.Fingerprint() {
		return guardDecision{Action: actRecreate, Reason: "policy_mismatch"}
	}
	if desiredImage == "" || facts.ImageID == "" {
		return deny("image_identity_unavailable")
	}
	if facts.ImageID != desiredImage {
		return guardDecision{Action: actRecreate, Reason: "image_mismatch"}
	}
	return guardDecision{Action: actReuse, Reason: "policy_equal"}
}

// ensureGuarded creates, reuses or recycles the container for a plan. The
// caller holds the folder lock. slotHeld says the caller holds the folder's
// agent slot; reconcile is the startup path that may replace an image mismatch.
func (r *Runner) ensureGuarded(ctx context.Context, plan *policyPlan, slotHeld, reconcile bool) (string, error) {
	m := r.managed
	if m == nil || m.engine == nil {
		return "", fmt.Errorf("%w: managed runtime not configured", ErrPolicyUnknown)
	}
	folder := plan.Policy.Scope.Folder
	facts, err := m.engine.InspectContainer(ctx, plan.Name)
	if err != nil {
		return "", fmt.Errorf("%w: inspect: %v", ErrPolicyUnknown, err)
	}
	var image string
	if facts != nil && facts.Running {
		if image, err = m.engine.ImageID(ctx, plan.Conf.Image); err != nil {
			return "", fmt.Errorf("%w: image: %v", ErrPolicyUnknown, err)
		}
	}
	dec := decideContainer(plan, facts, image, m.universe(r))
	switch dec.Action {
	case actDeny:
		r.log.Warn("container policy cannot be established, not reusing",
			slog.String("chat", folder), slog.String("role", string(plan.Policy.Role)), slog.String("reason", dec.Reason))
		return "", dec.Err
	case actReuse:
		return facts.ID, nil
	case actRecreate:
		if dec.Reason == "image_mismatch" && !reconcile {
			return "", &ImageMismatchError{}
		}
		if facts.Running {
			if !slotHeld {
				return "", ErrRecycleNeedsSlot
			}
			if m.holds == nil {
				return "", fmt.Errorf("%w: hold state unavailable", ErrExecutionUnproven)
			}
			if m.holds(folder) {
				return "", ErrExecutionUnproven
			}
		}
		r.log.Info("recycling container for a different execution policy",
			slog.String("chat", folder),
			slog.String("role", string(plan.Policy.Role)),
			slog.String("reason", dec.Reason),
			slog.String("id", shortID(facts.ID)))
		if err := m.engine.StopRemove(ctx, facts.ID); err != nil {
			return "", fmt.Errorf("runner: recycle %q: %w", folder, err)
		}
	}
	id, err := m.engine.CreateStart(ctx, plan.Name, plan.Conf, plan.Host)
	if err != nil {
		return "", err
	}
	return id, nil
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}
