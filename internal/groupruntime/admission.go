package groupruntime

import (
	"context"
	"slices"
	"strings"

	"github.com/Coriol-is/gopod/internal/runner"
	"github.com/Coriol-is/gopod/internal/store"
)

// Admission modes. Registered is the default (D047): the owner's /register is the
// permission. Allowlist keeps the stricter CP-020 gate for operators who want it.
const (
	AdmissionRegistered = "registered"
	AdmissionAllowlist  = "allowlist"

	// DefaultMaxGroups bounds prepared groups so a flood of registrations cannot
	// create unbounded runtime state.
	DefaultMaxGroups = 50
	maxGroupsCeiling = 500

	groupOwnerDisabledKey = store.GroupOwnerDisabledKey
)

// Is lets the runner classify any refused group start as an unavailable profile,
// so groups get one neutral notice instead of internals.
func (e *AdmissionError) Is(target error) bool { return target == runner.ErrProfileUnavailable }

func (c Config) validAdmission() bool {
	switch c.AdmissionMode {
	case "", AdmissionRegistered, AdmissionAllowlist:
	default:
		return false
	}
	return c.MaxGroups >= 0 && c.MaxGroups <= maxGroupsCeiling
}

// Mode returns the effective admission mode. An unknown value is neither mode and
// admits nothing.
func (c Config) Mode() string {
	if c.AdmissionMode == "" {
		return AdmissionRegistered
	}
	return c.AdmissionMode
}

func (c Config) registered() bool { return c.Mode() == AdmissionRegistered }

// GroupLimit is the effective cap on prepared groups in registered mode.
func (c Config) GroupLimit() int {
	if c.MaxGroups <= 0 {
		return DefaultMaxGroups
	}
	return c.MaxGroups
}

func groupChat(c store.RegisteredChat) bool { return !c.IsOwner && strings.HasPrefix(c.JID, "tg:-") }

// groupAllowed is the mode-specific part of Authorize. Allowlist mode requires
// the JID in EnabledGroups; registered mode requires only that the group is
// within the prepared-group cap.
func (m *Manager) groupAllowed(ctx context.Context, c store.RegisteredChat) bool {
	if !m.Config.registered() {
		return m.Config.Mode() == AdmissionAllowlist && slices.Contains(m.Config.EnabledGroups, c.JID)
	}
	prepared, err := m.preparedGroups(ctx)
	if err != nil {
		return false
	}
	rank := slices.IndexFunc(prepared, func(p store.RegisteredChat) bool { return p.JID == c.JID })
	return rank >= 0 && rank < m.Config.GroupLimit()
}

// preparedGroups lists groups that own a clean epoch, oldest registration first.
func (m *Manager) preparedGroups(ctx context.Context) ([]store.RegisteredChat, error) {
	rows, err := m.Store.ListRegistered(ctx)
	if err != nil {
		return nil, err
	}
	var out []store.RegisteredChat
	for _, c := range rows {
		if groupChat(c) && c.NamespaceEpoch > 1 {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b store.RegisteredChat) int {
		if a.AddedAt != b.AddedAt {
			return int(a.AddedAt - b.AddedAt)
		}
		return strings.Compare(a.JID, b.JID)
	})
	return out, nil
}

// canPrepare enforces the prepared-group cap before a new clean epoch exists.
func (m *Manager) canPrepare(ctx context.Context, c store.RegisteredChat) bool {
	if !m.Config.registered() || c.NamespaceEpoch > 1 {
		return true
	}
	prepared, err := m.preparedGroups(ctx)
	return err == nil && len(prepared) < m.Config.GroupLimit()
}

// Reason returns a short fixed label for why a registered chat is not admitted,
// or "" when only the heavier runtime checks remain. Labels hold no paths,
// provider details or personal data and are meant for the owner.
func (m *Manager) Reason(ctx context.Context, jid string) string {
	c, err := m.Store.GetRegistered(ctx, jid)
	switch {
	case err != nil:
		return "not registered"
	case !groupChat(c):
		return "not a group"
	case m.Config.Qualification == "":
		return "runtime not qualified"
	case !c.GroupEpochReady:
		return "clean epoch not ready" + m.lastStage(jid)
	}
	if known, e := m.Store.IsKnownGroup(ctx, jid); e != nil || !known {
		return "group not observed yet"
	}
	if v, _ := m.Store.GetState(ctx, groupOwnerDisabledKey+c.NamespaceID); v == "1" {
		return "disabled by owner"
	}
	if v, _ := m.Store.GetState(ctx, "group_disabled:"+c.NamespaceID); v == "1" {
		return "disabled"
	}
	if v, _ := m.Store.GetState(ctx, "group_cleanup_unknown:"+c.NamespaceID); v == "1" {
		return "cleanup unverified"
	}
	if !m.groupAllowed(ctx, c) {
		if m.Config.registered() {
			return "over prepared-group limit"
		}
		return "not in allowlist"
	}
	return ""
}

// listedForEnable applies the allowlist only in allowlist mode.
func (m *Manager) listedForEnable(jid string) bool {
	if m.Config.registered() {
		return true
	}
	return m.Config.Mode() == AdmissionAllowlist && slices.Contains(m.Config.EnabledGroups, jid)
}
