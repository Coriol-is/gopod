package queue

import "log/slog"

// holdOrdinary retains blocked input without holding a native/global slot. The
// reason is reconstructed from durable result evidence on every restart. There
// is deliberately no blind release API: reconcile/publicize the stored result,
// then restart; never clear evidence to force another model call.
func (q *Queue) holdOrdinary(folder, reason string, batch []Item) {
	q.mu.Lock()
	cs := q.chats[folder]
	if cs == nil {
		cs = &chatState{}
		q.chats[folder] = cs
	}
	if cs.ordinaryHold == "" {
		cs.ordinaryHold = reason
		cs.held++
	}
	if len(batch) > 0 {
		cs.pending = append(append([]Item(nil), batch...), cs.pending...)
	}
	q.signalDrainLocked()
	q.mu.Unlock()
	q.log.Warn("queue: ordinary work held for reconciliation", slog.String("folder", folder), slog.String("reason", reason))
}

// OrdinaryHolds reports non-executing folder barriers without exposing prompts.
func (q *Queue) OrdinaryHolds() map[string]string {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make(map[string]string)
	for folder, cs := range q.chats {
		if cs.ordinaryHold != "" {
			out[folder] = cs.ordinaryHold
		}
	}
	return out
}
