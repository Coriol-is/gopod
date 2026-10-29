package groupruntime

import (
	"context"
	"errors"
	"fmt"
	"github.com/Coriol-is/gopod/internal/store"
	"os"
	"path/filepath"
	"strings"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/runner/chattmpl"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
)

func (m *Manager) Status(ctx context.Context, jid string) (string, error) {
	rows, err := m.Store.ListRegistered(ctx)
	if err != nil {
		return "", err
	}
	var out []string
	for _, c := range rows {
		if !strings.HasPrefix(c.JID, "tg:-") || jid != "" && c.JID != jid {
			continue
		}
		state := "unavailable"
		if reason := m.Reason(ctx, c.JID); reason != "" {
			state = "unavailable (" + reason + ")"
		} else if m.Authorize(ctx, c) == nil {
			state = "enabled"
			for name, cfg := range m.Config.Providers {
				if _, _, _, e := m.qualifiedCatalog(name, cfg, ""); e != nil {
					state = "configuration unavailable"
					break
				}
			}
		}
		out = append(out, fmt.Sprintf("%s: %s; epoch=%d ready=%t", c.JID, state, c.NamespaceEpoch, c.GroupEpochReady))
	}
	if len(out) == 0 {
		return "No matching registered groups.", nil
	}
	header := fmt.Sprintf("admission=%s max_groups=%d", m.Config.Mode(), m.Config.GroupLimit())
	return header + "\n" + strings.Join(out, "\n"), nil
}
func (m *Manager) Prepare(ctx context.Context, jid string) (string, error) {
	if m.Store == nil || m.Docker == nil {
		return "", ErrUnavailable
	}
	epoch, paused, err := m.prepareScope(ctx, jid)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Prepared clean group epoch %d; %d old schedules paused. Historical data preserved. Group remains disabled until enable.", epoch, paused), nil
}

// prepareCore returns only the number of schedules paused.
func (m *Manager) prepareCore(ctx context.Context, jid string) (int64, error) {
	_, paused, err := m.prepareScope(ctx, jid)
	return paused, err
}

func (m *Manager) prepareScope(ctx context.Context, jid string) (epoch, paused int64, err error) {
	c, err := m.Store.GetRegistered(ctx, jid)
	if err != nil {
		return 0, 0, ErrUnavailable
	}
	unlock, err := m.lock(ctx, c.NamespaceID)
	if err != nil {
		return 0, 0, err
	}
	defer unlock()
	if err = m.Store.ValidateScope(ctx, c.Scope()); err != nil || c.IsOwner || !strings.HasPrefix(c.JID, "tg:-") {
		return 0, 0, ErrUnavailable
	}
	if known, e := m.Store.IsKnownGroup(ctx, jid); e != nil || !known {
		return 0, 0, ErrUnavailable
	}
	if err = m.Store.SetNamespaceDisabled(ctx, c.Scope(), true); err != nil {
		return 0, 0, err
	}
	if err = m.removeStoppedLegacy(ctx, c.Scope()); err != nil {
		return 0, 0, err
	}
	if err = m.requireInactiveNamespace(ctx, c.Scope()); err != nil {
		return 0, 0, err
	}
	if !m.canPrepare(ctx, c) {
		return 0, 0, ErrUnavailable
	}
	// Recover an interrupted preparation in the already allocated clean epoch.
	// Never rotate again, import old state or enable merely because roots exist.
	migration := store.GroupEpochMigration{CurrentScope: c.Scope()}
	if c.NamespaceEpoch <= 1 || c.GroupEpochReady {
		migration, err = m.Store.RotateGroupEpoch(ctx, jid, c.RegistrationRevision)
		if err != nil {
			return 0, 0, err
		}
	}
	paths, err := runner.PrepareNamespacePaths(m.DataDir, migration.CurrentScope, m.UID)
	if err != nil {
		return 0, 0, err
	}
	// Only new clean roots are seeded. Old custom files and all prior data remain
	// in their retired host-only scope; they are never followed or imported.
	if _, err = chattmpl.Seed(paths.Workspace, paths.Memory, chattmpl.Vars{ChatFolder: migration.CurrentScope.Folder}); err != nil {
		return 0, 0, err
	}
	return migration.CurrentScope.NamespaceEpoch, migration.PausedTasks, nil
}

// Enable is the owner's explicit re-enable; it also clears the owner kill switch.
func (m *Manager) Enable(ctx context.Context, jid string) (string, error) {
	if m.Store == nil || m.Docker == nil {
		return "", ErrUnavailable
	}
	text, err := m.enableCore(ctx, jid, false)
	if err != nil {
		return "", err
	}
	c, err := m.Store.GetRegistered(ctx, jid)
	if err != nil {
		return "", ErrUnavailable
	}
	if err = m.Store.SetState(ctx, groupOwnerDisabledKey+c.NamespaceID, "0"); err != nil {
		return "", err
	}
	return text, nil
}

// enableCore does the shared enabling steps. With auto set, the final write is a
// store-level CAS that refuses to enable a group the owner has disabled.
func (m *Manager) enableCore(ctx context.Context, jid string, auto bool) (string, error) {
	c, err := m.Store.GetRegistered(ctx, jid)
	if err != nil {
		return "", ErrUnavailable
	}
	unlock, err := m.lock(ctx, c.NamespaceID)
	if err != nil {
		return "", err
	}
	defer unlock()
	if m.Store.ValidateScope(ctx, c.Scope()) != nil || c.IsOwner || !strings.HasPrefix(c.JID, "tg:-") {
		return "", ErrUnavailable
	}
	// Validate catalog/default before any epoch completion or admission reset.
	for name, cfg := range m.Config.Providers {
		if _, _, _, e := m.qualifiedCatalog(name, cfg, ""); e != nil {
			return "", ErrUnavailable
		}
	}
	// Explicit enable may repair only qualified native/tool orphans. Legacy
	// permissive containers are never removed to make the guard pass.
	if err = m.requireNoLegacyNamespace(ctx, c.Scope()); err != nil {
		return "", err
	}
	if err = m.reconcile(ctx, c.NamespaceID); err != nil {
		return "", ErrUnavailable
	}
	if err = m.requireInactiveNamespace(ctx, c.Scope()); err != nil {
		return "", err
	}
	if !m.listedForEnable(jid) || m.Config.Qualification == "" || len(m.Config.Providers) == 0 {
		return "", ErrUnavailable
	}
	if known, err := m.Store.IsKnownGroup(ctx, jid); err != nil || !known {
		return "", ErrUnavailable
	}
	paths, err := runner.ValidateNamespacePaths(m.DataDir, c.Scope(), m.UID)
	if err != nil {
		return "", ErrUnavailable
	}
	// Enabling cannot quietly provision credentials, change billing or enable an
	// account service. Require explicit separate image/auth grants already present.
	for name, p := range m.Config.Providers {
		image, _, e := m.Docker.ImageInspectWithRaw(ctx, p.Image)
		if e != nil || image.Config == nil || image.Config.Labels["org.gopod.profile"] != "group" || image.Config.Labels["org.gopod.provider"] != name || image.Config.Labels["org.gopod.native-policy"] != runner.GroupNativePolicyVersion {
			return "", ErrUnavailable
		}
		// Verify exactly the same no-follow auth-only reference used by Start.
		// No token values, home directories or account configuration are copied.
		reference, e := m.authReference(p.AuthFile, paths.Policy)
		if e != nil {
			return "", ErrUnavailable
		}
		if e = os.Remove(reference); e != nil {
			return "", ErrUnavailable
		}
	}

	if err = m.Store.SetState(ctx, "group_cleanup_unknown:"+c.NamespaceID, "0"); err != nil {
		return "", err
	}
	currentScope := c.Scope()
	if !c.GroupEpochReady {
		currentScope, err = m.Store.CompleteGroupEpoch(ctx, c.Scope())
		if err != nil {
			return "", err
		}
	}
	if auto {
		err = m.Store.EnableNamespaceUnlessOwnerDisabled(ctx, currentScope)
	} else {
		err = m.Store.SetNamespaceDisabled(ctx, currentScope, false)
	}
	if err != nil {
		return "", err
	}
	return "Group isolation enabled for the qualified configuration. Personal services remain unavailable.", nil
}
func (m *Manager) Disable(ctx context.Context, jid string) (string, error) {
	if m.Store == nil {
		return "", ErrUnavailable
	}
	c, err := m.Store.GetRegistered(ctx, jid)
	if err != nil {
		return "", ErrUnavailable
	}
	// Owner kill switch first: automatic preparation must never re-enable it.
	if err = m.Store.SetState(ctx, groupOwnerDisabledKey+c.NamespaceID, "1"); err != nil {
		return "", err
	}
	if err = m.Store.SetNamespaceDisabled(ctx, c.Scope(), true); errors.Is(err, store.ErrStaleScope) {
		// A concurrent preparation step moved the revision; retry on the fresh scope.
		if c, err = m.Store.GetRegistered(ctx, jid); err == nil {
			err = m.Store.SetNamespaceDisabled(ctx, c.Scope(), true)
		}
	}
	if err != nil {
		return "", err
	}
	return "New group work disabled. Active work may settle; no historical data was deleted.", nil
}
func (m *Manager) NamespacePath(folder string) (string, bool) {
	c, err := m.Store.GetByFolder(context.Background(), folder)
	if err != nil {
		return "", false
	}
	if strings.HasPrefix(c.JID, "tg:-") {
		if m.Authorize(context.Background(), c) != nil {
			return "", false
		}
		p, err := runner.ValidateNamespacePaths(m.DataDir, c.Scope(), m.UID)
		if err != nil {
			return "", false
		}
		return p.IPC, true
	}
	// Caller additionally applies Runner.CheckAdmission for the actual owner.
	return filepath.Join(m.DataDir, "ipc", folder), true
}

func containerList(id string) container.ListOptions {
	return container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "gopod.group-native="+id))}
}

// requireInactiveNamespace checks current and retired storage aliases. Unknown
// Docker state fails closed; this never kills a foreground process or deletes a
// legacy container to make migration/enable appear safe.
func (m *Manager) requireInactiveNamespace(ctx context.Context, scope store.ScopeRef) error {
	if m.Store.ValidateScope(ctx, scope) != nil {
		return ErrUnavailable
	}
	folders, err := m.Store.ListNamespaceFolders(ctx, scope.NamespaceID)
	if err != nil || len(folders) == 0 {
		return ErrUnavailable
	}
	// A prior revoked lease can still own host-side memory/export work after all
	// Docker containers have disappeared. Do not retire its epoch until settled.
	if m.MCP != nil {
		for _, folder := range folders {
			if err = m.MCP.DrainScope(ctx, folder); err != nil {
				return ErrUnavailable
			}
		}
	}

	native, err := m.Docker.ContainerList(ctx, containerList(scope.NamespaceID))
	if err != nil || len(native) > 0 {
		return ErrUnavailable
	}
	for _, folder := range folders {
		tools, err := m.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", "gopod.group-tool="+folder))})
		if err != nil || len(tools) > 0 {
			return ErrUnavailable
		}
		legacy, err := m.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", runner.LabelChat+"="+folder))})
		if err != nil || len(legacy) > 0 {
			return ErrUnavailable
		}
	}
	return m.Store.ValidateScope(ctx, scope)
}

// A legacy personal-profile container may not be terminated or silently relabeled
// by repair. Only native/tool containers created by this namespace boundary are
// eligible for explicit orphan reconciliation under the controller's slots.
func (m *Manager) requireNoLegacyNamespace(ctx context.Context, scope store.ScopeRef) error {
	if m.Store.ValidateScope(ctx, scope) != nil {
		return ErrUnavailable
	}
	folders, err := m.Store.ListNamespaceFolders(ctx, scope.NamespaceID)
	if err != nil || len(folders) == 0 {
		return ErrUnavailable
	}
	for _, folder := range folders {
		found, err := m.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", runner.LabelChat+"="+folder))})
		if err != nil {
			return ErrUnavailable
		}
		for _, c := range found {
			if c.Labels["gopod.group-native"] != scope.NamespaceID && c.Labels["gopod.group-tool"] != folder {
				return ErrUnavailable
			}
		}
	}
	return m.Store.ValidateScope(ctx, scope)
}

// removeStoppedLegacy deletes stopped personal-profile containers left under this
// namespace's keys so they cannot block preparation. It never stops or removes a
// running, restarting or paused container, and uses no force flag; each removal is
// logged. Their volumes and the group's data are untouched.
func (m *Manager) removeStoppedLegacy(ctx context.Context, scope store.ScopeRef) error {
	folders, err := m.Store.ListNamespaceFolders(ctx, scope.NamespaceID)
	if err != nil || len(folders) == 0 {
		return ErrUnavailable
	}
	for _, folder := range folders {
		found, e := m.Docker.ContainerList(ctx, container.ListOptions{All: true, Filters: filters.NewArgs(filters.Arg("label", runner.LabelChat+"="+folder))})
		if e != nil {
			return ErrUnavailable
		}
		for _, f := range found {
			if f.State != "exited" && f.State != "created" && f.State != "dead" {
				continue
			}
			if e = m.Docker.ContainerRemove(ctx, f.ID, container.RemoveOptions{}); e != nil {
				return ErrUnavailable
			}
			if m.Log != nil {
				m.Log.Info("removed stopped legacy group container", "namespace", scope.NamespaceID, "container", sanitizeName(f.ID[:min(12, len(f.ID))]))
			}
		}
	}
	return nil
}
