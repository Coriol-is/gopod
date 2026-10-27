package runner

import (
	"context"
	"strconv"
	"strings"

	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"github.com/Coriol-is/gopod/internal/store"
)

// GroupRuntime supplies the separately qualified trusted-inference / untrusted-
// tools boundary. No default group implementation or personal fallback exists.
type GroupRuntime interface {
	Authorize(context.Context, store.RegisteredChat) error
	Start(context.Context, store.RegisteredChat, AgentProvider, int, string) (string, AgentProvider, func(), error)
}

func (r *Runner) SetGroupRuntime(g GroupRuntime) { r.groups = g }
func isGroupRegistration(c store.RegisteredChat) bool {
	if c.IsOwner || !strings.HasPrefix(c.JID, "tg:") {
		return false
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(c.JID, "tg:"), 10, 64)
	return err == nil && id < 0 && c.JID == "tg:"+strconv.FormatInt(id, 10)
}
func (r *Runner) groupRegistration(ctx context.Context, folder string) (store.RegisteredChat, bool) {
	if r.profilePolicy == nil || r.profilePolicy.registrations == nil {
		return store.RegisteredChat{}, false
	}
	rows, err := r.profilePolicy.registrations.ListRegistered(ctx)
	if err != nil {
		return store.RegisteredChat{}, false
	}
	var found store.RegisteredChat
	count := 0
	for _, c := range rows {
		if c.Folder == folder {
			found = c
			count++
		}
	}
	return found, count == 1 && isGroupRegistration(found)
}
func (r *Runner) turnRuntime(ctx context.Context, folder string, thread int, tier Tier, al *mountsec.Allowlist) (string, AgentProvider, func(), error) {
	if c, ok := r.groupRegistration(ctx, folder); ok {
		if r.groups == nil {
			return "", nil, func() {}, ErrProfileUnavailable
		}
		return r.groups.Start(ctx, c, r.ProviderForChat(folder), thread, r.ModelForChat(folder))
	}
	id, err := r.ensure(ctx, folder, tier, al)
	return id, r.ProviderForChat(folder), func() {}, err
}

func validateExecution(ctx context.Context, prov AgentProvider) error {
	if checked, ok := prov.(interface{ ValidateExecution(context.Context) error }); ok {
		return checked.ValidateExecution(ctx)
	}
	return nil
}

// WithChatSlot serializes host-owned isolation migration/reconciliation with
// foreground, recovery and management execution. It never cancels foreground work.
func (r *Runner) WithChatSlot(ctx context.Context, folder string, operation func(context.Context) (string, error)) (string, error) {
	release, err := r.acquireAgentSlot(ctx, folder)
	if err != nil {
		return "", err
	}
	defer release()
	return operation(ctx)
}
