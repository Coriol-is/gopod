package runner

import (
	"context"
	"fmt"
	"path/filepath"
)

// ChatWorkspace resolves only an admitted, current registration. Group sources
// come from protected roots; no missing/retired group falls back to legacy paths.
// Personal paths retain their existing layout and customization behavior.
func (r *Runner) ChatWorkspace(ctx context.Context, folder string) (string, error) {
	if err := r.authorizeProfile(ctx, folder); err != nil {
		return "", err
	}
	c, group, err := r.groupRegistration(ctx, folder)
	if err != nil {
		return "", err
	}
	if group {
		if !c.GroupEpochReady {
			return "", ErrProfileUnavailable
		}
		paths, err := ValidateNamespacePaths(r.paths.DataDir, c.Scope(), NamespaceHostUID())
		if err != nil {
			return "", ErrProfileUnavailable
		}
		return paths.Workspace, nil
	}
	// A group can be retired while authorization is being checked. A failed
	// group lookup must not turn into a personal/legacy path fallback.
	rows, err := r.profilePolicy.registrations.ListRegistered(ctx)
	if err != nil {
		return "", ErrProfileUnavailable
	}
	ownerJID := fmt.Sprintf("tg:%d", r.profilePolicy.ownerID)
	count := 0
	for _, c := range rows {
		if c.Folder != folder {
			continue
		}
		count++
		if c.JID != ownerJID || !c.IsOwner {
			return "", ErrProfileUnavailable
		}
	}
	if count != 1 {
		return "", ErrProfileUnavailable
	}
	return filepath.Join(r.paths.ChatsDir, folder), nil
}
