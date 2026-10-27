package groupruntime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/docker/docker/api/types/container"
)

// Pin an explicit auth-only file inode under a host-owned directory. No personal
// home/settings/history is copied. The readonly native mount cannot modify the
// shared credential; refresh persistence stays with the owner's private runtime.
func (m *Manager) authReference(source, policy string) (string, error) {
	if !filepath.IsAbs(source) || filepath.Clean(source) != source {
		return "", ErrUnavailable
	}
	f, err := os.OpenFile(source, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return "", ErrUnavailable
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 65536 || info.Mode().Perm()&0077 != 0 {
		return "", ErrUnavailable
	}
	st, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(st.Uid) != m.UID {
		return "", ErrUnavailable
	}
	temp, err := os.CreateTemp(policy, "auth-ref-*")
	if err != nil {
		return "", ErrUnavailable
	}
	name := temp.Name()
	_ = temp.Close()
	_ = os.Remove(name)
	if err = os.Link(source, name); err != nil {
		return "", ErrUnavailable
	}
	linked, err := os.Lstat(name)
	if err != nil || !linked.Mode().IsRegular() || !os.SameFile(info, linked) {
		_ = os.Remove(name)
		return "", ErrUnavailable
	}
	return name, nil
}

func (m *Manager) verify(ctx context.Context, id, image, fingerprint, networkID string, expected *container.HostConfig) error {
	c, err := m.Docker.ContainerInspect(ctx, id)
	if err != nil || c.Config == nil || c.HostConfig == nil || c.State == nil || !c.State.Running {
		return ErrUnavailable
	}
	h := c.HostConfig
	if c.Image != image || c.Config.Labels["gopod.group-policy"] != fingerprint || c.Config.User != m.user() || c.Config.WorkingDir != runner.GroupInferenceDir || h.Privileged || !h.ReadonlyRootfs || len(h.CapAdd) > 0 || len(h.ExtraHosts) > 0 || h.NetworkMode != container.NetworkMode(networkID) || len(h.PortBindings) > 0 || h.PublishAllPorts || len(h.Devices) > 0 {
		return ErrUnavailable
	}
	if len(h.CapDrop) != 1 || strings.ToUpper(h.CapDrop[0]) != "ALL" || len(c.Mounts) != len(expected.Mounts) || c.NetworkSettings == nil || len(c.NetworkSettings.Networks) != 1 {
		return ErrUnavailable
	}
	for _, want := range expected.Mounts {
		found := false
		for _, got := range c.Mounts {
			if got.Source == want.Source && got.Destination == want.Target && got.RW == !want.ReadOnly {
				found = true
			}
		}
		if !found {
			return ErrUnavailable
		}
	}
	found := false
	for _, s := range h.SecurityOpt {
		if s == "no-new-privileges:true" || s == "no-new-privileges" {
			found = true
		}
	}
	if !found {
		return ErrUnavailable
	}
	return nil
}
