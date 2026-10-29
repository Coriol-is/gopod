package runner

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
)

// containerEngine implementation for the real Docker daemon (CP-004 PR 3).
var _ containerEngine = (*Docker)(nil)

// InspectContainer reports the configuration of the container called name, or
// nil facts when there is none. Env VALUES are dropped here: only names are
// returned, so nothing downstream can log or hash a secret.
func (d *Docker) InspectContainer(ctx context.Context, name string) (*ContainerFacts, error) {
	id, err := d.inspectByName(ctx, name)
	if err != nil {
		return nil, err
	}
	if id == "" {
		return nil, nil
	}
	info, err := d.cli.ContainerInspect(ctx, id)
	if err != nil {
		return nil, fmt.Errorf("docker: inspect %q: %w", name, err)
	}
	f := &ContainerFacts{ID: id, ImageID: info.Image}
	if info.State != nil {
		f.Running = info.State.Running
	}
	if info.Config == nil || info.HostConfig == nil {
		return f, nil // Inspected stays false: the guard fails closed
	}
	f.Labels = info.Config.Labels
	f.EnvNames = envNamesOf(info.Config.Env)
	f.User = info.Config.User
	f.ReadonlyRootfs = info.HostConfig.ReadonlyRootfs
	f.Privileged = info.HostConfig.Privileged
	f.CapDrop = []string(info.HostConfig.CapDrop)
	f.SecurityOpt = info.HostConfig.SecurityOpt
	for _, m := range info.Mounts {
		if m.Type != mount.TypeBind {
			continue
		}
		f.Mounts = append(f.Mounts, Mount{Source: m.Source, Target: m.Destination, ReadOnly: !m.RW})
	}
	f.Inspected = true
	return f, nil
}

// ImageID returns the immutable image ID the reference currently names.
func (d *Docker) ImageID(ctx context.Context, ref string) (string, error) {
	img, err := d.cli.ImageInspect(ctx, ref)
	if err != nil {
		return "", fmt.Errorf("docker: inspect image: %w", err)
	}
	return img.ID, nil
}

// StopRemove stops (gracefully) and removes a container. Bind-mounted host
// state (native credentials, sessions, workspace) is not touched.
func (d *Docker) StopRemove(ctx context.Context, id string) error {
	if err := d.Stop(ctx, id, DefaultStopGrace); err != nil {
		return err
	}
	return d.Remove(ctx, id)
}

// CreateStart creates and starts a container; a half-created one is removed.
func (d *Docker) CreateStart(ctx context.Context, name string, cfg *container.Config, host *container.HostConfig) (string, error) {
	created, err := d.cli.ContainerCreate(ctx, cfg, host, nil, nil, name)
	if err != nil {
		return "", fmt.Errorf("docker: create %q: %w", name, err)
	}
	if err := d.cli.ContainerStart(ctx, created.ID, container.StartOptions{}); err != nil {
		_ = d.cli.ContainerRemove(ctx, created.ID, container.RemoveOptions{Force: true})
		return "", fmt.Errorf("docker: start %q: %w", name, err)
	}
	d.log.Info("container spawned", "name", name, "id", strings.TrimSpace(shortID(created.ID)))
	return created.ID, nil
}
