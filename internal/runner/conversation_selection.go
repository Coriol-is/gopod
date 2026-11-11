package runner

import (
	"context"
	"errors"
	"fmt"
	"github.com/Coriol-is/gopod/internal/runner/mountsec"
	"slices"

	"github.com/Coriol-is/gopod/internal/store"
)

type conversationSelectionStore interface {
	ResolveDefaultSelection(context.Context, string, string) (store.ConversationSelection, error)
	ResolveConversationSelection(context.Context, string, int, string) (store.ConversationSelection, error)
	ChangeConversationSelection(context.Context, store.ConversationSelection, string, string) (store.ConversationSelection, error)
	BindTurnSelection(context.Context, int64, int, store.ConversationSelection) (store.ConversationSelection, error)
}
type selectedConversationKey struct{}
type selectionLoginKey struct{}
type selectionAttemptsKey struct{}
type selectedConversation struct {
	selection store.ConversationSelection
	provider  AgentProvider
}

// EnableConversationSelection installs the immutable provider registry at startup.
// It never changes runtime concurrency or durable-session mapping.
func (r *Runner) EnableConversationSelection(providers map[string]AgentProvider) {
	r.providersMu.Lock()
	defer r.providersMu.Unlock()
	r.conversationProviders = make(map[string]AgentProvider, len(providers))
	for k, v := range providers {
		r.conversationProviders[k] = v
	}
}
func WithSelectionAttempts(ctx context.Context, members []store.ResultMember) context.Context {
	return context.WithValue(ctx, selectionAttemptsKey{}, slices.Clone(members))
}
func (r *Runner) ConversationSelection(ctx context.Context, folder string, thread int) (store.ConversationSelection, error) {
	s, ok := r.store.(conversationSelectionStore)
	if !ok {
		return store.ConversationSelection{}, errors.New("runner: conversation selection store unavailable")
	}
	return s.ResolveConversationSelection(ctx, folder, thread, r.provider.Name())
}
func (r *Runner) ChangeConversationSelection(ctx context.Context, expected store.ConversationSelection, kind, value string) (store.ConversationSelection, error) {
	if err := r.authorizeProfile(ctx, expected.Scope.Folder); err != nil {
		return store.ConversationSelection{}, err
	}
	if err := r.selectionGuard(ctx, expected.Scope.Folder); err != nil {
		return store.ConversationSelection{}, err
	}
	release, err := r.acquireAgentSlot(ctx, expected.Scope.Folder)
	if err != nil {
		return store.ConversationSelection{}, err
	}
	defer release()
	if err := r.selectionGuard(ctx, expected.Scope.Folder); err != nil {
		return store.ConversationSelection{}, err
	}
	if err = r.authorizeProfile(ctx, expected.Scope.Folder); err != nil {
		return store.ConversationSelection{}, err
	}
	if (kind == "provider" || kind == "default_provider") && value != "" {
		r.providersMu.RLock()
		p := r.conversationProviders[value]
		r.providersMu.RUnlock()
		if p == nil {
			return store.ConversationSelection{}, errors.New("runner: unknown provider")
		}
	}
	s, ok := r.store.(conversationSelectionStore)
	if !ok {
		return store.ConversationSelection{}, errors.New("runner: conversation selection store unavailable")
	}
	out, err := s.ChangeConversationSelection(ctx, expected, kind, value)
	if err == nil {
		r.providersMu.Lock()
		r.invalidateStartupLocked(expected.Scope.Folder)
		r.providersMu.Unlock()
	}
	return out, err
}

// selectConversation runs only after acquiring the folder's agent slot. Every
// native operation uses this same context snapshot until it releases that slot.
func (r *Runner) selectConversation(ctx context.Context, folder string, thread int) (context.Context, error) {
	r.providersMu.RLock()
	enabled := r.conversationProviders != nil
	r.providersMu.RUnlock()
	if !enabled {
		return ctx, nil
	}
	if ctx.Value(selectionLoginKey{}) != true {
		if err := r.selectionGuard(ctx, folder); err != nil {
			return ctx, err
		}
	}
	var selected store.ConversationSelection
	var err error
	if thread < 0 {
		selected, err = r.store.(conversationSelectionStore).ResolveDefaultSelection(ctx, folder, r.provider.Name())
	} else {
		selected, err = r.ConversationSelection(ctx, folder, thread)
	}
	if err != nil {
		return ctx, err
	}
	if members, ok := ctx.Value(selectionAttemptsKey{}).([]store.ResultMember); ok {
		st := r.store.(conversationSelectionStore)
		for i, m := range members {
			saved, e := st.BindTurnSelection(ctx, m.TurnID, m.Attempt, selected)
			if e != nil {
				return ctx, e
			}
			if i > 0 && (saved.Provider != selected.Provider || saved.Model != selected.Model || saved.Revision != selected.Revision) {
				return ctx, errors.New("runner: batch selection mismatch")
			}
			selected = saved
		}
	}
	r.providersMu.RLock()
	p := r.conversationProviders[selected.Provider]
	r.providersMu.RUnlock()
	if p == nil {
		return ctx, fmt.Errorf("runner: unavailable selected provider %q", selected.Provider)
	}
	return context.WithValue(ctx, selectedConversationKey{}, selectedConversation{selected, p}), nil
}
func (r *Runner) providerForContext(ctx context.Context, folder string) AgentProvider {
	if v, ok := ctx.Value(selectedConversationKey{}).(selectedConversation); ok && v.selection.Scope.Folder == folder {
		return v.provider
	}
	return r.ProviderForChat(folder)
}
func (r *Runner) modelForContext(ctx context.Context, folder string) string {
	if v, ok := ctx.Value(selectedConversationKey{}).(selectedConversation); ok && v.selection.Scope.Folder == folder {
		return v.selection.Model
	}
	return r.ModelForChat(folder)
}

func (r *Runner) ConversationSelectionEnabled() bool {
	r.providersMu.RLock()
	defer r.providersMu.RUnlock()
	return r.conversationProviders != nil
}

// SetConversationSelectionGuard installs the controller's native-login fence.
// Configure before admitting external work; checks are never authorization.
func (r *Runner) SetConversationSelectionGuard(guard func(context.Context, string) error) {
	r.providersMu.Lock()
	r.conversationSelectionGuard = guard
	r.providersMu.Unlock()
}
func (r *Runner) selectionGuard(ctx context.Context, folder string) error {
	r.providersMu.RLock()
	g := r.conversationSelectionGuard
	r.providersMu.RUnlock()
	if g != nil {
		return g(ctx, folder)
	}
	return nil
}

// PrepareConversationLogin holds admission through selection and allocation. The
// controller's already-reserved login slot fences subsequent model work.
func (r *Runner) PrepareConversationLogin(ctx context.Context, folder string, thread int, tier Tier, al *mountsec.Allowlist) (string, AgentProvider, error) {
	release, ok := r.tryIdleSlot(folder)
	if !ok {
		return "", nil, ErrFolderBusy
	}
	defer release()
	ctx = context.WithValue(ctx, selectionLoginKey{}, true)
	ctx, err := r.selectConversation(ctx, folder, thread)
	if err != nil {
		return "", nil, err
	}
	if err = r.verifyQuiescent(ctx, folder); err != nil {
		return "", nil, err
	}
	id, err := r.ensure(ctx, folder, tier, al)
	return id, r.providerForContext(ctx, folder), err
}
func (r *Runner) CheckConversationAuth(ctx context.Context, folder string, thread int, expected string, tier Tier, al *mountsec.Allowlist) (AuthStatus, error) {
	release, err := r.acquireAgentSlot(ctx, folder)
	if err != nil {
		return AuthStatus{}, err
	}
	defer release()
	ctx, err = r.selectConversation(ctx, folder, thread)
	if err != nil {
		return AuthStatus{}, err
	}
	p := r.providerForContext(ctx, folder)
	if p.Name() != expected {
		return AuthStatus{}, ErrProviderChanged
	}
	if err = r.verifyQuiescent(ctx, folder); err != nil {
		return AuthStatus{}, err
	}
	id, err := r.ensure(ctx, folder, tier, al)
	if err != nil {
		return AuthStatus{}, err
	}
	return r.checkAuthViaProvider(ctx, id, p)
}
