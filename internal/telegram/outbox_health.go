package telegram

// Running reports local dispatcher lifecycle only, not remote delivery success.
// It is safe for a read-only readiness probe and never wakes a worker.
func (d *OutboxDispatcher) Running() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.done == nil {
		return false
	}
	select {
	case <-d.done:
		return false
	default:
		return true
	}
}
