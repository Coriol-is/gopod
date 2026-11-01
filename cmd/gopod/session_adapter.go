package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/Coriol-is/gopod/internal/store"
)

// runnerStateStore mirrors runner's unexported stateStore so main.go can pass
// either implementation to agentRunner.SetStore.
type runnerStateStore interface {
	GetState(ctx context.Context, key string) (string, error)
	SetState(ctx context.Context, key, value string) error
	GetSession(ctx context.Context, folder string, thread int, provider string) (string, bool, error)
	PutSession(ctx context.Context, folder string, thread int, provider, id string) error
	DeleteSession(ctx context.Context, folder string, thread int, provider string) error
}

// durableSessionStore satisfies runnerStateStore in durable mode: provider
// session ids live in native contexts under the conversation's active
// execution session and context epoch.
type durableSessionStore struct{ st *store.Store }

// newSessionStateStore picks the session store by the persisted mapping mode:
// the store itself (legacy sessions table) or the durable adapter.
func newSessionStateStore(ctx context.Context, st *store.Store, log *slog.Logger) (runnerStateStore, string, error) {
	mode, _, err := st.SessionMappingMode(ctx)
	if err != nil {
		return nil, "", fmt.Errorf("reading session mapping mode: %w", err)
	}
	log.Info("session mapping", slog.String("mode", mode))
	if mode == store.SessionMappingDurable {
		return &durableSessionStore{st: st}, mode, nil
	}
	return st, mode, nil
}

func (d *durableSessionStore) GetState(ctx context.Context, key string) (string, error) {
	return d.st.GetState(ctx, key)
}

func (d *durableSessionStore) SetState(ctx context.Context, key, value string) error {
	return d.st.SetState(ctx, key, value)
}

func (d *durableSessionStore) binding(ctx context.Context, folder string, thread int) (store.ScopeRef, store.ConversationBinding, error) {
	scope, err := d.st.SnapshotScope(ctx, folder)
	if err != nil {
		return store.ScopeRef{}, store.ConversationBinding{}, fmt.Errorf("snapshot scope for %q: %w", folder, err)
	}
	b, err := d.st.ResolveConversation(ctx, scope, thread)
	if err != nil {
		return store.ScopeRef{}, store.ConversationBinding{}, fmt.Errorf("resolve conversation %q/%d: %w", folder, thread, err)
	}
	return scope, b, nil
}

func (d *durableSessionStore) GetSession(ctx context.Context, folder string, thread int, provider string) (string, bool, error) {
	_, b, err := d.binding(ctx, folder, thread)
	if err != nil {
		return "", false, err
	}
	id, ok, err := d.st.GetNativeContext(ctx, b.ActiveSessionID, b.ContextEpoch, provider)
	if err != nil {
		return "", false, fmt.Errorf("get native context: %w", err)
	}
	return id, ok, nil
}

func (d *durableSessionStore) PutSession(ctx context.Context, folder string, thread int, provider, id string) error {
	scope, b, err := d.binding(ctx, folder, thread)
	if err != nil {
		return err
	}
	if err := d.st.PutNativeContext(ctx, scope, b.ActiveSessionID, b.ContextEpoch, provider, id); err != nil {
		return fmt.Errorf("put native context: %w", err)
	}
	return nil
}

func (d *durableSessionStore) DeleteSession(ctx context.Context, folder string, thread int, provider string) error {
	scope, b, err := d.binding(ctx, folder, thread)
	if err != nil {
		return err
	}
	if err := d.st.DeleteNativeContext(ctx, scope, b.ActiveSessionID, b.ContextEpoch, provider); err != nil {
		return fmt.Errorf("delete native context: %w", err)
	}
	return nil
}
