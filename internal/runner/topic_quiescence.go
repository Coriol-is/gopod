package runner

import "context"

// BeginConversationQuiescence cancels only the named conversation, including
// runs waiting for the shared folder slot. It never kills a shared container.
func (r *Runner) BeginConversationQuiescence(folder string, thread int) uint64 {
	return r.conversations.Block(folder, thread)
}

// WaitConversationQuiescent joins native leases. Existing execution-unknown or
// quarantine evidence is a blocker even when the Go handler has returned.
func (r *Runner) WaitConversationQuiescent(ctx context.Context, folder string, thread int) error {
	if err := r.conversations.Wait(ctx, folder, thread); err != nil {
		return err
	}
	if r.ExecutionUncertain(ctx, folder) {
		return ErrExecutionUnproven
	}
	return nil
}
func (r *Runner) ReopenConversation(folder string, thread int, generation uint64) error {
	return r.conversations.Reopen(folder, thread, generation)
}

// ForgetRetiredConversationCounters removes only the retired topic's transient
// auto-compact count. Call after joining its native work and persisting retirement;
// shared folder activity, sibling counts and admission fences remain intact.
func (r *Runner) ForgetRetiredConversationCounters(folder string, thread int) {
	if thread <= 0 {
		return
	}
	r.turnCountMu.Lock()
	defer r.turnCountMu.Unlock()
	delete(r.turnCount, convKey{folder, thread})
}
