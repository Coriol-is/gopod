package runner

import (
	"context"
	"errors"
	"fmt"

	"github.com/Coriol-is/gopod/internal/store"
)

// ErrProfileUnavailable means the runtime has not qualified this security profile.
// It intentionally contains no workspace, credential or personal-path details.
var ErrProfileUnavailable = errors.New("runner: agent profile unavailable; shared-chat isolation is not yet qualified")

type registrationReader interface {
	ListRegistered(context.Context) ([]store.RegisteredChat, error)
}

type admissionPolicy struct {
	ownerID       int64
	registrations registrationReader
}

// SetAdmissionPolicy installs host-owned identity resolution before serving work.
// P0 admits only the configured positive Telegram private-chat identity. Neither
// caller tiers nor mutable workspace claims grant personal authority. Other
// profiles remain closed until CP-020 qualification; there is no sandbox bypass.
func (r *Runner) SetAdmissionPolicy(ownerID int64, registrations registrationReader) {
	r.profilePolicy = &admissionPolicy{ownerID: ownerID, registrations: registrations}
}

// CheckAdmission lets host IPC and startup paths share the runtime identity gate.
func (r *Runner) CheckAdmission(ctx context.Context, folder string) error {
	return r.authorizeProfile(ctx, folder)
}

func (r *Runner) authorizeProfile(ctx context.Context, folder string) error {
	if r.profilePolicy == nil || r.profilePolicy.ownerID <= 0 || r.profilePolicy.registrations == nil || folder == "" {
		return ErrProfileUnavailable
	}
	chats, err := r.profilePolicy.registrations.ListRegistered(ctx)
	if err != nil {
		return ErrProfileUnavailable
	}
	ownerJID := fmt.Sprintf("tg:%d", r.profilePolicy.ownerID)
	count := 0
	for _, chat := range chats {
		if chat.Folder != folder {
			continue
		}
		count++
		if chat.JID != ownerJID || !chat.IsOwner {
			if r.groups == nil || !isGroupRegistration(chat) || r.groups.Authorize(ctx, chat) != nil {
				return ErrProfileUnavailable
			}
		}
	}
	if count != 1 {
		return ErrProfileUnavailable
	}
	return nil
}

// containerEnvAllow is defense in depth: personal credentials are assembled only
// for the host-verified personal tier. Restricted runtimes inherit no global env.
func containerEnvAllow(tier Tier, prov AgentProvider, extra []string) []string {
	if tier != TierOwner {
		return nil
	}
	result := append([]string(nil), prov.RequiredEnvVars()...)
	return append(result, extra...)
}
