package groupruntime

import (
	"context"
	"os"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/network"
)

// reconcile is called only while the namespace and Runner chat slots are held,
// before a new live lease is granted (or by explicit owner enable/repair). It
// sweeps owned orphans, confirms settlement, then removes only current-scope
// temporary policy material. Native/session/history/data roots are never pruned.
func (m *Manager) reconcile(ctx context.Context, namespace string) (retErr error) {
	if m.Store == nil || m.Docker == nil {
		return ErrUnavailable
	}
	rows, err := m.Store.ListRegistered(ctx)
	if err != nil {
		return ErrUnavailable
	}
	var current store.RegisteredChat
	matches := 0
	for _, c := range rows {
		if c.NamespaceID == namespace {
			current = c
			matches++
		}
	}
	if matches != 1 || current.IsOwner || !strings.HasPrefix(current.JID, "tg:-") || m.Store.ValidateScope(ctx, current.Scope()) != nil {
		return ErrUnavailable
	}
	defer func() {
		if retErr != nil {
			mark, done := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			defer done()
			_ = m.Store.SetState(mark, "group_cleanup_unknown:"+namespace, "1")
		}
	}()
	paths, err := runner.ValidateNamespacePaths(m.DataDir, current.Scope(), m.UID)
	if err != nil {
		return ErrUnavailable
	}
	folders, err := m.Store.ListNamespaceFolders(ctx, namespace)
	if err != nil || len(folders) == 0 {
		return ErrUnavailable
	}
	cleanup, done := context.WithTimeout(ctx, 20*time.Second)
	defer done()
	// A timed-out lease may still own host-side memory/export work even after
	// every Docker process is gone. Drain tracked current/retired scopes first.
	if m.MCP != nil {
		for _, folder := range folders {
			if err = m.MCP.DrainScope(cleanup, folder); err != nil {
				return ErrUnavailable
			}
		}
	}
	old, err := m.namespaceOrphans(cleanup, namespace, folders)
	if err != nil {
		return ErrUnavailable
	}
	for _, id := range old {
		if err = m.Docker.ContainerRemove(cleanup, id, container.RemoveOptions{Force: true}); err != nil {
			return ErrUnavailable
		}
	}
	remaining, err := m.namespaceOrphans(cleanup, namespace, folders)
	if err != nil || len(remaining) != 0 {
		return ErrUnavailable
	}
	nets, err := m.Docker.NetworkList(cleanup, network.ListOptions{Filters: filters.NewArgs(filters.Arg("label", "gopod.group-native="+namespace))})
	if err != nil {
		return ErrUnavailable
	}
	for _, n := range nets {
		if n.ID == "" || n.Labels["gopod.group-native"] != namespace {
			return ErrUnavailable
		}
		if err = m.Docker.NetworkRemove(cleanup, n.ID); err != nil {
			return ErrUnavailable
		}
	}
	nets, err = m.Docker.NetworkList(cleanup, network.ListOptions{Filters: filters.NewArgs(filters.Arg("label", "gopod.group-native="+namespace))})
	if err != nil || len(nets) != 0 || m.Store.ValidateScope(ctx, current.Scope()) != nil {
		return ErrUnavailable
	}
	// Revalidate rooted sources after Docker settlement before touching host files.
	checked, err := runner.ValidateNamespacePaths(m.DataDir, current.Scope(), m.UID)
	if err != nil || checked != paths {
		return ErrUnavailable
	}
	return m.prunePolicyTemps(paths.Policy)
}

func (m *Manager) namespaceOrphans(ctx context.Context, namespace string, folders []string) ([]string, error) {
	allowed := make(map[string]bool, len(folders))
	for _, folder := range folders {
		allowed[folder] = true
	}
	ids := map[string]bool{}
	native, err := m.Docker.ContainerList(ctx, containerList(namespace))
	if err != nil {
		return nil, err
	}
	for _, c := range native {
		if c.ID == "" || c.Labels["gopod.group-native"] != namespace {
			return nil, ErrUnavailable
		}
		if folder := c.Labels[runner.LabelChat]; folder != "" && !allowed[folder] {
			return nil, ErrUnavailable
		}
		ids[c.ID] = true
	}
	for _, folder := range folders {
		tools, err := m.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "gopod.group-tool="+folder))})
		if err != nil {
			return nil, err
		}
		for _, c := range tools {
			if c.ID == "" || c.Labels["gopod.group-tool"] != folder {
				return nil, ErrUnavailable
			}
			if other := c.Labels[runner.LabelChat]; other != "" && other != folder {
				return nil, ErrUnavailable
			}
			ids[c.ID] = true
		}
	}
	out := make([]string, 0, len(ids))
	for id := range ids {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

func (m *Manager) prunePolicyTemps(path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return ErrUnavailable
	}
	defer root.Close()
	f, err := root.OpenFile(".", os.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return ErrUnavailable
	}
	entries, err := f.ReadDir(-1)
	f.Close()
	if err != nil {
		return ErrUnavailable
	}
	var directories, files []string
	for _, entry := range entries {
		name := entry.Name()
		turn := strings.HasPrefix(name, "turn-") && len(name) > len("turn-")
		auth := strings.HasPrefix(name, "auth-ref-") && len(name) > len("auth-ref-")
		if !turn && !auth {
			continue
		}
		info, err := root.Lstat(name)
		if err != nil {
			return ErrUnavailable
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != m.UID || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnavailable
		}
		if turn && info.IsDir() {
			directories = append(directories, name)
			continue
		}
		if auth && info.Mode().IsRegular() {
			files = append(files, name)
			continue
		}
		return ErrUnavailable
	}
	for _, name := range directories {
		if err = root.RemoveAll(name); err != nil {
			return ErrUnavailable
		}
	}
	// Removing a pinned auth reference unlinks only that hardlink, never its source.
	for _, name := range files {
		if err = root.Remove(name); err != nil {
			return ErrUnavailable
		}
	}
	return nil
}
