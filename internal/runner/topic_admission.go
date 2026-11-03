package runner

import (
	"context"

	"github.com/Coriol-is/gopod/internal/store"
)

// validateTopicAdmission runs after the per-chat slot is acquired, so a turn
// waiting behind other work cannot reuse its earlier admission decision. It is
// not a quiescence lease; deletion must still wait for existing native work.
// Production SetAdmissionPolicy receives *store.Store; synthetic/legacy readers
// without scope authority retain their existing admission behavior.
func (r *Runner) validateTopicAdmission(ctx context.Context, folder string, thread int) error {
	if thread <= 0 || r.profilePolicy == nil {
		return nil
	}
	authority, ok := r.profilePolicy.registrations.(interface {
		SnapshotScope(context.Context, string) (store.ScopeRef, error)
		ValidateConversation(context.Context, store.ConversationScope) error
	})
	if !ok {
		return nil
	}
	scope, err := authority.SnapshotScope(ctx, folder)
	if err != nil {
		return err
	}
	return authority.ValidateConversation(ctx, store.ConversationScope{Scope: scope, ThreadID: thread, Visibility: store.VisibilityConversation})
}
