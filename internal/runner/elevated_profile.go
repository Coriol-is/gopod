package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
)

// ElevatedFolder is reserved for explicitly confirmed host-administration sessions.
const ElevatedFolder = "elevated"

// ElevatedSpawnConfig cannot be constructed from a chat SpawnConfig. No caller
// supplied mounts or environment are accepted; the elevated path owns its profile.
type ElevatedSpawnConfig struct {
	Image, Version, Provider          string
	WorkspaceDir, ClaudeDir, CodexDir string
	MemoryBytes, NanoCPUs, PidsLimit  int64
}

func (c ElevatedSpawnConfig) validate() error {
	if c.Image == "" || c.Version == "" {
		return errors.New("elevated: image and version are required")
	}
	if c.Provider != "claude" && c.Provider != "codex" {
		return errors.New("elevated: unsupported provider")
	}
	for _, p := range []string{c.WorkspaceDir, c.ClaudeDir, c.CodexDir} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p || p == "/" {
			return errors.New("elevated: state paths must be clean absolute directories")
		}
	}
	if c.WorkspaceDir == c.ClaudeDir || c.WorkspaceDir == c.CodexDir || c.ClaudeDir == c.CodexDir {
		return errors.New("elevated: state directories must be distinct")
	}
	if c.MemoryBytes <= 0 || c.NanoCPUs <= 0 || c.PidsLimit <= 0 {
		return errors.New("elevated: positive resource limits are required")
	}
	return nil
}

func elevatedMounts(c ElevatedSpawnConfig) []Mount {
	return []Mount{
		{Source: "/", Target: "/host"},
		{Source: "/var/run/docker.sock", Target: "/var/run/docker.sock"},
		{Source: c.WorkspaceDir, Target: "/workspace/session"},
		{Source: c.ClaudeDir, Target: "/home/node/.claude"},
		{Source: c.CodexDir, Target: "/home/node/.codex"},
	}
}

// BuildElevatedContainerArgs grants host authority, not isolation. Ordinary
// BuildContainerArgs is unchanged and continues to reject root.
func BuildElevatedContainerArgs(c ElevatedSpawnConfig) (*container.Config, *container.HostConfig, string, error) {
	if err := c.validate(); err != nil {
		return nil, nil, "", err
	}
	conf := &container.Config{Image: c.Image, User: "0:0", WorkingDir: "/workspace/session",
		Env:    []string{"HOME=/home/node", "GOPOD_CHAT_FOLDER=" + ElevatedFolder},
		Labels: map[string]string{"gopod.chat": ElevatedFolder, "gopod.version": c.Version}}
	pids := c.PidsLimit
	host := &container.HostConfig{Privileged: true, PidMode: "host", NetworkMode: "host", IpcMode: "host",
		RestartPolicy: container.RestartPolicy{Name: "no"}, Mounts: toDockerMounts(elevatedMounts(c)),
		Resources: container.Resources{Memory: c.MemoryBytes, NanoCPUs: c.NanoCPUs, PidsLimit: &pids}}
	return conf, host, ContainerName(ElevatedFolder), nil
}

func elevatedPlan(c ElevatedSpawnConfig) (*policyPlan, error) {
	conf, host, name, err := BuildElevatedContainerArgs(c)
	if err != nil {
		return nil, err
	}
	mounts := elevatedMounts(c)
	// A reserved identity, never a fabricated registration or namespace snapshot.
	scope := store.ScopeRef{Folder: ElevatedFolder}
	pol := EffectiveExecutionPolicy{Role: RoleElevatedHost, Scope: scope, PolicyRevision: policyRevision,
		Mounts: mounts, Image: c.Image, Grants: roleGrants[RoleElevatedHost],
		Credentials: CredentialBinding{Provider: c.Provider, Kind: credNativeStateDir, Sources: stateDirSources(mounts)}}
	for k, v := range pol.Labels(digestOfSpawn(conf, host, mounts, nil)) {
		conf.Labels[k] = v
	}
	return &policyPlan{Policy: pol, Conf: conf, Host: host, Name: name, Mounts: mounts}, nil
}

// ensureElevatedContainer uses the Docker facade directly, independent of
// GOPOD_MANAGED_TASKS. The future session manager serializes calls. Existing
// containers are never recycled here: drift/death must close a live session.
func ensureElevatedContainer(ctx context.Context, engine containerEngine, c ElevatedSpawnConfig) (string, error) {
	if engine == nil {
		return "", errors.New("elevated: Docker is unavailable")
	}
	plan, err := elevatedPlan(c)
	if err != nil {
		return "", err
	}
	facts, err := engine.InspectContainer(ctx, plan.Name)
	if err != nil {
		return "", fmt.Errorf("elevated inspect: %w", err)
	}
	image, err := engine.ImageID(ctx, c.Image)
	if err != nil {
		return "", fmt.Errorf("elevated image: %w", err)
	}
	if image == "" {
		return "", fmt.Errorf("%w: elevated image identity unavailable", ErrPolicyUnknown)
	}
	if facts != nil && facts.Labels[LabelPolicyConfig] != plan.Conf.Labels[LabelPolicyConfig] {
		return "", fmt.Errorf("%w: elevated requested configuration differs", ErrPolicyUnknown)
	}
	decision := decideContainer(plan, facts, image, nil)
	switch decision.Action {
	case actCreate:
		return engine.CreateStart(ctx, plan.Name, plan.Conf, plan.Host)
	case actReuse:
		return facts.ID, nil
	default:
		return "", fmt.Errorf("%w: elevated container not reusable (%s)", ErrPolicyUnknown, decision.Reason)
	}
}
